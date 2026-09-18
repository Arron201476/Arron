const SAMPLE_RATE = 24000;
const MAX_CHUNK_BYTES = SAMPLE_RATE * 2;
const MAX_SESSION_BYTES = SAMPLE_RATE * 2 * 600;

export type VoicePlaybackResult = 'played' | 'stopped';

// One instance owns one context and one session. Resume the context in a user gesture.
export class VoicePlayback {
  private readonly sources = new Set<AudioBufferSourceNode>();
  private nextStart = 0;
  private totalBytes = 0;
  private ending = false;
  private stopped = false;
  private closing = false;
  private completion: Promise<VoicePlaybackResult> | undefined;
  private resolve: ((result: VoicePlaybackResult) => void) | undefined;
  private reject: ((reason: unknown) => void) | undefined;

  constructor(private readonly context: AudioContext) {}

  append(pcm: Uint8Array): void {
    if (this.ending || this.context.state === 'closed') throw new Error('Playback is closed');
    if (!pcm.byteLength || pcm.byteLength % 2 || pcm.byteLength > MAX_CHUNK_BYTES) {
      throw new Error('Invalid PCM chunk');
    }
    if (this.totalBytes + pcm.byteLength > MAX_SESSION_BYTES) throw new Error('Audio limit exceeded');
    const buffer = this.context.createBuffer(1, pcm.byteLength / 2, SAMPLE_RATE);
    const samples = buffer.getChannelData(0);
    const view = new DataView(pcm.buffer, pcm.byteOffset, pcm.byteLength);
    for (let i = 0; i < samples.length; i++) samples[i] = view.getInt16(i * 2, true) / 32768;
    const source = this.context.createBufferSource();
    source.buffer = buffer;
    const start = Math.max(this.nextStart, this.context.currentTime + 0.02);
    source.onended = () => {
      source.onended = null;
      source.disconnect();
      this.sources.delete(source);
      this.closeWhenDrained();
    };
    this.sources.add(source);
    try {
      source.connect(this.context.destination);
      source.start(start);
    } catch (error) {
      source.onended = null;
      this.sources.delete(source);
      source.disconnect();
      throw error;
    }
    this.totalBytes += pcm.byteLength;
    this.nextStart = start + buffer.duration;
  }

  finish(): Promise<VoicePlaybackResult> {
    this.ending = true;
    const result = this.result();
    this.closeWhenDrained();
    return result;
  }

  stop(): Promise<VoicePlaybackResult> {
    this.stopped = true;
    this.ending = true;
    const result = this.result();
    for (const source of this.sources) {
      source.onended = null;
      try { source.stop(); } catch { /* Closing the owned context also stops its sources. */ }
      source.disconnect();
    }
    this.sources.clear();
    this.closeWhenDrained();
    return result;
  }

  private result(): Promise<VoicePlaybackResult> {
    return this.completion ??= new Promise((resolve, reject) => {
      this.resolve = resolve;
      this.reject = reject;
    });
  }

  private closeWhenDrained(): void {
    if (!this.ending || this.sources.size || this.closing) return;
    this.closing = true;
    void (async () => {
      try {
        if (this.context.state !== 'closed') await this.context.close();
        this.resolve?.(this.stopped ? 'stopped' : 'played');
      } catch (error) {
        this.reject?.(error);
      }
    })();
  }
}
