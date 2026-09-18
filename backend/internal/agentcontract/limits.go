package agentcontract

// Native RunState includes binary tool outputs. Keep the checkpoint and its
// private transport bounded while allowing supported 10 MiB Skill resources.
const MaxSDKRunStateBytes = 64 << 20
const MaxSDKRunStateEnvelopeBytes = MaxSDKRunStateBytes + (1 << 20)
