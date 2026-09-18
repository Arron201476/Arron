import { afterEach, expect, it, vi } from 'vitest';

afterEach(() => vi.unstubAllGlobals());

async function processor(rate = 24000) {
  vi.resetModules();
  const postMessage = vi.fn();
  let Processor: any;
  vi.stubGlobal('sampleRate', rate);
  vi.stubGlobal('AudioWorkletProcessor', class { port = { postMessage, onmessage: null }; });
  vi.stubGlobal('registerProcessor', (_name: string, implementation: any) => { Processor = implementation; });
  await import('./voiceCaptureProcessor.js');
  return { instance: new Processor(), postMessage };
}

it('converts clamped samples to little endian PCM and silences output', async () => {
  const { instance, postMessage } = await processor();
  const output = new Float32Array([1, 1]);
  expect(instance.process([[new Float32Array([-2, 0, 2])]], [[output]])).toBe(true);
  expect([...new Uint8Array(postMessage.mock.calls[0][0])]).toEqual([0, 128, 0, 0, 255, 127]);
  expect([...output]).toEqual([0, 0]);
});

it('stops exactly at sixty seconds and acknowledges once', async () => {
  const { instance, postMessage } = await processor();
  for (let i = 0; i < 59; i++) expect(instance.process([[new Float32Array(24000)]], [])).toBe(true);
  expect(instance.process([[new Float32Array(24001)]], [])).toBe(false);
  expect(postMessage.mock.calls[59][0].byteLength).toBe(48000);
  instance.port.onmessage({ data: 'stop' });
  expect(postMessage.mock.calls.filter(([data]) => data === 'ended')).toHaveLength(1);
  expect(instance.process([[new Float32Array(1)]], [])).toBe(false);
});

it('rejects incorrect sample rates rather than relabeling audio', async () => {
  const { instance, postMessage } = await processor(48000);
  expect(instance.process([[new Float32Array(1)]], [])).toBe(false);
  expect(postMessage).toHaveBeenCalledWith('invalid_sample_rate');
});

it('rejects nonfinite samples', async () => {
  const { instance, postMessage } = await processor();
  expect(instance.process([[new Float32Array([NaN])]], [])).toBe(false);
  expect(postMessage).toHaveBeenCalledWith('invalid_samples');
});
