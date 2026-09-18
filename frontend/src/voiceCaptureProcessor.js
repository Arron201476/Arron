class VoiceCaptureProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    this.samples = 0;
    this.ended = false;
    this.port.onmessage = event => {
      if (event.data === 'stop') this.end();
    };
  }

  end() {
    if (this.ended) return;
    this.ended = true;
    this.port.postMessage('ended');
  }

  process(inputs, outputs) {
    // Keep the graph active without monitoring microphone audio through speakers.
    for (const output of outputs) for (const channel of output) channel.fill(0);
    if (this.ended) return false;
    if (sampleRate !== 24000) {
      this.port.postMessage('invalid_sample_rate');
      this.ended = true;
      return false;
    }
    const input = inputs[0]?.[0];
    if (!input?.length) return true;
    const length = Math.min(input.length, 24000 * 60 - this.samples);
    const pcm = new ArrayBuffer(length * 2);
    const view = new DataView(pcm);
    for (let i = 0; i < length; i++) {
      if (!Number.isFinite(input[i])) {
        this.port.postMessage('invalid_samples');
        this.ended = true;
        return false;
      }
      const value = Math.max(-1, Math.min(1, input[i]));
      view.setInt16(i * 2, Math.round(value * (value < 0 ? 32768 : 32767)), true);
    }
    this.samples += length;
    this.port.postMessage(pcm, [pcm]);
    if (this.samples === 24000 * 60) this.end();
    return !this.ended;
  }
}

registerProcessor('content-agent-voice-capture', VoiceCaptureProcessor);
