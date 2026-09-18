import { describe, expect, it, vi } from 'vitest';
import { VoicePlayback } from './voicePlayback';

function fixture(close = vi.fn(async () => {})) {
  const sources: Array<{
    buffer: AudioBuffer | null;
    onended: (() => void) | null;
    connect: ReturnType<typeof vi.fn>;
    disconnect: ReturnType<typeof vi.fn>;
    start: ReturnType<typeof vi.fn>;
    stop: ReturnType<typeof vi.fn>;
  }> = [];
  const channels: Float32Array[] = [];
  const context = {
    state: 'running', currentTime: 1, destination: {}, close,
    createBuffer: vi.fn((_channels: number, length: number, rate: number) => {
      const data = new Float32Array(length);
      channels.push(data);
      return { duration: length / rate, getChannelData: () => data };
    }),
    createBufferSource: vi.fn(() => {
      const source = { buffer: null, onended: null, connect: vi.fn(), disconnect: vi.fn(), start: vi.fn(), stop: vi.fn() };
      sources.push(source);
      return source;
    }),
  };
  return { playback: new VoicePlayback(context as unknown as AudioContext), context, sources, channels };
}

describe('VoicePlayback', () => {
  it('decodes little endian PCM including a nonzero byte offset', async () => {
    const f = fixture();
    f.playback.append(new Uint8Array([99, 0, 128, 255, 127, 0, 0, 99]).subarray(1, 7));
    expect([...f.channels[0]]).toEqual([-1, 32767 / 32768, 0]);
    expect(f.context.createBuffer).toHaveBeenCalledWith(1, 3, 24000);
    await f.playback.stop();
  });

  it('schedules contiguous chunks and waits for actual playback to drain', async () => {
    const f = fixture();
    f.playback.append(new Uint8Array(48000));
    f.playback.append(new Uint8Array(48000));
    expect(f.sources[0].start).toHaveBeenCalledWith(1.02);
    expect(f.sources[1].start).toHaveBeenCalledWith(2.02);
    const done = f.playback.finish();
    f.sources[0].onended?.();
    expect(f.context.close).not.toHaveBeenCalled();
    f.sources[1].onended?.();
    await expect(done).resolves.toBe('played');
    expect(f.context.close).toHaveBeenCalledTimes(1);
  });

  it('cancels queued sources and waits for context cleanup on stop', async () => {
    let release!: () => void;
    const f = fixture(vi.fn(() => new Promise<void>(resolve => { release = resolve; })));
    f.playback.append(new Uint8Array(2));
    const done = f.playback.finish();
    expect(f.playback.stop()).toBe(done);
    expect(f.playback.stop()).toBe(done);
    expect(f.sources[0].stop).toHaveBeenCalledTimes(1);
    expect(f.sources[0].onended).toBeNull();
    let settled = false;
    void done.then(() => { settled = true; });
    await Promise.resolve();
    expect(settled).toBe(false);
    release();
    await expect(done).resolves.toBe('stopped');
    expect(() => f.playback.append(new Uint8Array(2))).toThrow('closed');
  });

  it('surfaces context close failures', async () => {
    const f = fixture(vi.fn(async () => { throw new Error('close failed'); }));
    await expect(f.playback.stop()).rejects.toThrow('close failed');
  });

  it('rejects malformed and excessive chunks without creating sources', async () => {
    const f = fixture();
    for (const size of [0, 1, 48002]) expect(() => f.playback.append(new Uint8Array(size))).toThrow();
    expect(f.sources).toHaveLength(0);
    await f.playback.stop();
  });

  it('bounds the session even when earlier chunks have drained', async () => {
    const f = fixture();
    for (let i = 0; i < 600; i++) {
      f.playback.append(new Uint8Array(48000));
      f.sources[i].onended?.();
    }
    expect(() => f.playback.append(new Uint8Array(2))).toThrow('limit');
    await f.playback.finish();
  });

  it('keeps session cleanup separate', async () => {
    const old = fixture();
    const current = fixture();
    old.playback.append(new Uint8Array(2));
    current.playback.append(new Uint8Array(2));
    await old.playback.stop();
    expect(current.context.close).not.toHaveBeenCalled();
    expect(current.sources[0].stop).not.toHaveBeenCalled();
    await current.playback.stop();
  });
});
