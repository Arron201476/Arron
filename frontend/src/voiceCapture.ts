const MAX_BYTES = 24000 * 2 * 60;

export class VoiceCapture {
  private stream: MediaStream | undefined;
  private source: MediaStreamAudioSourceNode | undefined;
  private node: AudioWorkletNode | undefined;
  private chunks: Uint8Array[] = [];
  private bytes = 0;
  private started = false;
  private closed = false;
  private ended = false;
  private failure: Error | undefined;
  private closeTask: Promise<void> | undefined;
  private endSignal: (() => void) | undefined;

  // Construct and call start in a user gesture. The context is exclusively owned.
  constructor(private readonly context: AudioContext = new AudioContext({ sampleRate: 24000 })) {}

  async start(): Promise<void> {
    if (this.started || this.closed) throw new Error('Voice capture is not startable');
    this.started = true;
    try {
      if (this.context.sampleRate !== 24000) throw new Error('Unsupported capture sample rate');
      await this.context.resume();
      if (this.closed) throw new Error('Voice capture cancelled');
      const stream = await navigator.mediaDevices.getUserMedia({
        audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true }, video: false,
      });
      if (this.closed) {
        stream.getTracks().forEach(track => track.stop());
        throw new Error('Voice capture cancelled');
      }
      this.stream = stream;
      for (const track of stream.getAudioTracks()) {
        track.addEventListener('ended', () => {
          if (!this.closed) this.fail('Microphone input ended');
        }, { once: true });
      }
      await this.context.audioWorklet.addModule(new URL('./voiceCaptureProcessor.js', import.meta.url));
      if (this.closed) throw new Error('Voice capture cancelled');
      this.node = new AudioWorkletNode(this.context, 'content-agent-voice-capture', {
        numberOfInputs: 1, numberOfOutputs: 1, outputChannelCount: [1],
        channelCount: 1, channelCountMode: 'explicit',
      });
      this.node.port.onmessage = event => this.receive(event.data);
      this.node.onprocessorerror = () => this.fail('Voice audio processor failed');
      this.source = this.context.createMediaStreamSource(stream);
      this.source.connect(this.node);
      this.node.connect(this.context.destination);
    } catch (error) {
      await this.cancel();
      throw error;
    }
  }

  private receive(data: unknown): void {
    if (this.closed || this.ended) return;
    if (data === 'ended') {
      this.ended = true;
      this.endSignal?.();
      void this.close().catch(error => { this.failure = error instanceof Error ? error : new Error('Capture cleanup failed'); });
      return;
    }
    if (!(data instanceof ArrayBuffer) || !data.byteLength || data.byteLength % 2
      || data.byteLength > 48000 || this.bytes + data.byteLength > MAX_BYTES) {
      this.fail('Invalid capture audio');
      return;
    }
    this.chunks.push(new Uint8Array(data));
    this.bytes += data.byteLength;
  }

  private fail(message: string): void {
    this.failure = new Error(message);
    this.ended = true;
    this.endSignal?.();
    void this.cancel().catch(() => { /* finish still awaits the rejected close task. */ });
  }

  async finish(): Promise<Uint8Array> {
    if (!this.ended) {
      if (!this.node || this.closed) throw new Error('Voice capture is not recording');
      if (this.endSignal) throw new Error('Voice capture is already finishing');
      let timer: ReturnType<typeof setTimeout> | undefined;
      try {
        await new Promise<void>((resolve, reject) => {
          this.endSignal = resolve;
          timer = setTimeout(() => reject(new Error('Capture stop timed out')), 1000);
          this.node!.port.postMessage('stop');
        });
      } catch (error) {
        await this.cancel();
        throw error;
      } finally {
        clearTimeout(timer);
      }
    }
    await this.close();
    if (this.failure) throw this.failure;
    if (!this.bytes) throw new Error('Voice capture is empty');
    const result = new Uint8Array(this.bytes);
    let offset = 0;
    for (const chunk of this.chunks) { result.set(chunk, offset); offset += chunk.byteLength; }
    this.chunks = [];
    this.bytes = 0;
    return result;
  }

  cancel(): Promise<void> {
    this.failure ??= new Error('Voice capture cancelled');
    this.chunks = [];
    this.bytes = 0;
    this.ended = true;
    this.endSignal?.();
    return this.close();
  }

  private close(): Promise<void> {
    if (this.closeTask) return this.closeTask;
    this.closed = true;
    this.stream?.getTracks().forEach(track => track.stop());
    this.source?.disconnect();
    if (this.node) {
      this.node.port.onmessage = null;
      this.node.onprocessorerror = null;
      this.node.port.close();
      this.node.disconnect();
    }
    return this.closeTask = this.context.state === 'closed' ? Promise.resolve() : this.context.close();
  }
}
