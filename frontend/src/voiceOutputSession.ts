import type { VoicePlayback, VoicePlaybackResult } from './voicePlayback';

type Playback = Pick<VoicePlayback, 'append' | 'finish' | 'stop'>;

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Invalid voice message');
  return value as Record<string, unknown>;
}

function exact(value: Record<string, unknown>, keys: string[]): void {
  const actual = Object.keys(value);
  if (actual.length !== keys.length || actual.some(key => !keys.includes(key))) {
    throw new Error('Invalid voice message fields');
  }
}

// Only accepts the Go public projection, never Sidecar control or approval frames.
export class VoiceOutputSession {
  private sequence = 1;
  private ended = false;
  private stopped = false;
  private completion: Promise<VoicePlaybackResult> | undefined;

  constructor(
    private readonly sessionId: string,
    private readonly generation: number,
    private readonly playback: Playback,
  ) {
    if (!sessionId || sessionId.length > 256 || sessionId.trim() !== sessionId
      || /[\u0000-\u001f\u007f]/.test(sessionId)
      || !Number.isSafeInteger(generation) || generation < 1) {
      throw new Error('Invalid voice session binding');
    }
  }

  async receive(raw: string): Promise<void> {
    try {
      if (this.ended || this.stopped) throw new Error('Voice output is closed');
      if (typeof raw !== 'string' || raw.length > 96 * 1024
        || new TextEncoder().encode(raw).byteLength > 96 * 1024) {
        throw new Error('Voice message is oversized');
      }
      const envelope = object(JSON.parse(raw));
      exact(envelope, ['session_id', 'generation', 'sequence', 'payload']);
      if (envelope.session_id !== this.sessionId || envelope.generation !== this.generation
        || !Number.isSafeInteger(envelope.sequence) || envelope.sequence !== this.sequence) {
        throw new Error('Voice message binding or sequence mismatch');
      }
      const payload = object(envelope.payload);
      switch (payload.type) {
        case 'audio': {
          exact(payload, ['type', 'format', 'sample_rate', 'channels', 'audio']);
          if (payload.format !== 'pcm16' || payload.sample_rate !== 24000 || payload.channels !== 1
            || typeof payload.audio !== 'string' || !payload.audio.length || payload.audio.length > 64000) {
            throw new Error('Invalid voice audio format');
          }
          const decoded = atob(payload.audio);
          if (decoded.length % 2 || btoa(decoded) !== payload.audio) throw new Error('Invalid voice PCM encoding');
          this.playback.append(Uint8Array.from(decoded, character => character.charCodeAt(0)));
          break;
        }
        case 'turn_started':
        case 'turn_ended':
        case 'session_ended':
          exact(payload, ['type']);
          this.ended = payload.type === 'session_ended';
          break;
        default:
          throw new Error('Nonpublic voice event');
      }
      this.sequence++;
      if (this.ended) {
        this.completion = this.playback.finish();
        await this.completion;
      }
    } catch {
      try { await this.stop(); } catch (cleanupError) {
        throw new AggregateError([new Error('Voice output failed'), cleanupError], 'Voice output and cleanup failed');
      }
      // Do not expose parser text, which can contain untrusted wire content.
      throw new Error('Voice output failed');
    }
  }

  stop(): Promise<VoicePlaybackResult> {
    if (!this.stopped) {
      this.stopped = true;
      this.completion = this.playback.stop();
    }
    return this.completion!;
  }

  async transportClosed(): Promise<VoicePlaybackResult> {
    if (this.stopped || this.ended) return this.completion!;
    await this.stop();
    throw new Error('Voice transport closed before session completion');
  }
}
