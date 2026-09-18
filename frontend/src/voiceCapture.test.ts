import { afterEach, describe, expect, it, vi } from 'vitest';
import { VoiceCapture } from './voiceCapture';

afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers(); });

function fixture() {
  const track = { stop: vi.fn(), addEventListener: vi.fn() };
  const stream = { getTracks: () => [track], getAudioTracks: () => [track] };
  const getUserMedia = vi.fn(async () => stream);
  vi.stubGlobal('navigator', { mediaDevices: { getUserMedia } });
  const node = {
    port: { onmessage: null as ((event: { data: unknown }) => void) | null, postMessage: vi.fn(), close: vi.fn() },
    onprocessorerror: null as (() => void) | null, connect: vi.fn(), disconnect: vi.fn(),
  };
  vi.stubGlobal('AudioWorkletNode', class { constructor() { return node; } });
  const source = { connect: vi.fn(), disconnect: vi.fn() };
  const context = {
    sampleRate: 24000, state: 'running', resume: vi.fn(async () => {}), close: vi.fn(async () => {}),
    audioWorklet: { addModule: vi.fn(async () => {}) }, destination: {}, createMediaStreamSource: vi.fn(() => source),
  };
  const recorder = new VoiceCapture(context as unknown as AudioContext);
  const send = (data: unknown) => node.port.onmessage?.({ data });
  return { recorder, context, node, track, stream, getUserMedia, send };
}

describe('VoiceCapture', () => {
  it('collects PCM and awaits the processor stop acknowledgement', async () => {
    const f = fixture();
    await f.recorder.start();
    f.send(new Uint8Array([0, 128]).buffer);
    const result = f.recorder.finish();
    expect(f.node.port.postMessage).toHaveBeenCalledWith('stop');
    expect(f.track.stop).not.toHaveBeenCalled();
    f.send(new Uint8Array([255, 127]).buffer);
    f.send('ended');
    await expect(result).resolves.toEqual(new Uint8Array([0, 128, 255, 127]));
    expect(f.track.stop).toHaveBeenCalledTimes(1);
    expect(f.context.close).toHaveBeenCalledTimes(1);
    expect(f.node.port.onmessage).toBeNull();
  });

  it('stops late permission results after cancellation', async () => {
    const f = fixture();
    let resolve!: (value: typeof f.stream) => void;
    f.getUserMedia.mockImplementation(() => new Promise(done => { resolve = done; }));
    const starting = f.recorder.start();
    await Promise.resolve();
    await f.recorder.cancel();
    resolve(f.stream);
    await expect(starting).rejects.toThrow('cancelled');
    expect(f.track.stop).toHaveBeenCalledTimes(1);
    expect(f.context.audioWorklet.addModule).not.toHaveBeenCalled();
  });

  it('closes context when microphone permission is denied', async () => {
    const f = fixture();
    f.getUserMedia.mockRejectedValue(new Error('permission denied'));
    await expect(f.recorder.start()).rejects.toThrow('permission denied');
    expect(f.context.close).toHaveBeenCalledTimes(1);
  });

  it('releases the microphone if worklet loading fails', async () => {
    const f = fixture();
    f.context.audioWorklet.addModule.mockRejectedValue(new Error('module failed'));
    await expect(f.recorder.start()).rejects.toThrow('module failed');
    expect(f.track.stop).toHaveBeenCalledTimes(1);
  });

  it('rejects invalid processor output and discards previously recorded audio', async () => {
    const f = fixture();
    await f.recorder.start();
    f.send(new Uint8Array([0, 0]).buffer);
    f.send(new ArrayBuffer(1));
    await expect(f.recorder.finish()).rejects.toThrow('Invalid capture audio');
    expect(f.track.stop).toHaveBeenCalledTimes(1);
  });

  it('bounds stop acknowledgement waits and releases resources', async () => {
    vi.useFakeTimers();
    const f = fixture();
    await f.recorder.start();
    const result = expect(f.recorder.finish()).rejects.toThrow('timed out');
    await vi.advanceTimersByTimeAsync(1000);
    await result;
    expect(f.track.stop).toHaveBeenCalledTimes(1);
  });

  it('automatically closes after the processor duration limit', async () => {
    const f = fixture();
    await f.recorder.start();
    f.send(new Uint8Array([0, 0]).buffer);
    f.send('ended');
    expect(f.track.stop).toHaveBeenCalledTimes(1);
    await expect(f.recorder.finish()).resolves.toEqual(new Uint8Array([0, 0]));
  });

  it('rejects a different context sample rate before asking for microphone access', async () => {
    const f = fixture();
    f.context.sampleRate = 48000;
    await expect(f.recorder.start()).rejects.toThrow('sample rate');
    expect(f.getUserMedia).not.toHaveBeenCalled();
  });
});
