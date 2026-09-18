import { describe, expect, it, vi } from 'vitest';
import { VoiceOutputSession } from './voiceOutputSession';

const audio = { type: 'audio', format: 'pcm16', sample_rate: 24000, channels: 1, audio: 'AIA=' };
const frame = (payload: unknown, sequence = 1, binding = {}) => JSON.stringify({
  session_id: 'voice-1', generation: 1, sequence, payload, ...binding,
});
function fixture() {
  const playback = {
    append: vi.fn(), finish: vi.fn(async () => 'played' as const), stop: vi.fn(async () => 'stopped' as const),
  };
  return { playback, session: new VoiceOutputSession('voice-1', 1, playback) };
}

describe('VoiceOutputSession', () => {
  it('delivers validated PCM and waits for playback on terminal output', async () => {
    const { session, playback } = fixture();
    await session.receive(frame({ type: 'turn_started' }));
    await session.receive(frame(audio, 2));
    expect(playback.append).toHaveBeenCalledWith(new Uint8Array([0, 128]));
    await session.receive(frame({ type: 'turn_ended' }, 3));
    expect(playback.finish).not.toHaveBeenCalled();
    await session.receive(frame({ type: 'session_ended' }, 4));
    await expect(session.transportClosed()).resolves.toBe('played');
    expect(playback.stop).not.toHaveBeenCalled();
  });

  it.each([
    frame(audio, 2), frame(audio, 1, { session_id: 'other' }), frame(audio, 1, { generation: 2 }),
    frame(audio, 1, { private: true }), frame({ ...audio, sample_rate: 48000 }),
    frame({ ...audio, audio: 'AI==' }), frame({ ...audio, audio: 'AIA' }),
    frame({ ...audio, audio: 'AIA=\n' }), frame({ ...audio, audio: '' }),
    frame({ type: 'voice_control_request' }), frame({ type: 'session_ended', secret: true }),
    'null', '[]', '{"secret":', ' '.repeat(96 * 1024 + 1),
  ])('rejects invalid output and closes playback', async raw => {
    const { session, playback } = fixture();
    await expect(session.receive(raw)).rejects.toThrow('Voice output failed');
    expect(playback.append).not.toHaveBeenCalled();
    expect(playback.stop).toHaveBeenCalledTimes(1);
    await expect(session.receive(frame(audio))).rejects.toThrow();
    expect(playback.stop).toHaveBeenCalledTimes(1);
  });

  it('rejects replays and audio after completion', async () => {
    const a = fixture();
    await a.session.receive(frame(audio));
    await expect(a.session.receive(frame(audio))).rejects.toThrow();
    expect(a.playback.append).toHaveBeenCalledTimes(1);
    const b = fixture();
    await b.session.receive(frame({ type: 'session_ended' }));
    await expect(b.session.receive(frame(audio, 2))).rejects.toThrow();
    expect(b.playback.append).not.toHaveBeenCalled();
  });

  it('does not treat a disconnect without terminal output as success', async () => {
    const { session, playback } = fixture();
    await session.receive(frame(audio));
    await expect(session.transportClosed()).rejects.toThrow('before session completion');
    expect(playback.stop).toHaveBeenCalledTimes(1);
  });

  it('stops an in-progress drain and keeps stop idempotent', async () => {
    const { session, playback } = fixture();
    let resolve!: (value: 'stopped') => void;
    const completion = new Promise<'stopped'>(done => { resolve = done; });
    playback.finish.mockImplementation(() => completion as never);
    playback.stop.mockImplementation(() => { resolve('stopped'); return completion; });
    const receiving = session.receive(frame({ type: 'session_ended' }));
    await session.stop();
    await receiving;
    await expect(session.transportClosed()).resolves.toBe('stopped');
    await session.stop();
    expect(playback.stop).toHaveBeenCalledTimes(1);
  });

  it('preserves failure when playback cleanup also fails', async () => {
    const { session, playback } = fixture();
    playback.stop.mockRejectedValue(new Error('cleanup failed'));
    await expect(session.receive('null')).rejects.toBeInstanceOf(AggregateError);
  });
});
