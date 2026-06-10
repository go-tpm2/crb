# go-tpm2/crb

A pure-Go TPM 2.0 **Command Response Buffer (CRB)** MMIO transport.

`crb` implements [`github.com/go-tpm2/common`](https://github.com/go-tpm2/common)'s
`Transport` interface over its `Regs` MMIO accessor. The platform owns the
register mapping (a physical MMIO window, an `mmap` of `/dev/mem`, or a test
stub) and exposes it through `common.Regs`; this package drives the CRB
register handshake defined by the TCG **PC Client Platform TPM Profile (PTP)
Specification**, clause *Command Response Buffer Interface*.

## Usage

```go
import (
    "github.com/go-tpm2/common"
    "github.com/go-tpm2/crb"
)

// r is a platform-provided common.Regs over the CRB control area base.
tpm, err := crb.Open(r)
if err != nil {
    // not a CRB interface, or the interface never reported valid
}

cmd := common.BuildCommand(uint16(common.TagNoSessions),
    uint32(common.CCGetRandom), []byte{0x00, 0x02})
rsp, err := tpm.Send(cmd) // common.Transport
```

## Send state machine (TCG PTP)

1. **Request locality** — `LOC_CTRL.requestAccess`, wait
   `LOC_STS.Granted` + `LOC_STATE.locAssigned`.
2. **Request command-ready** — `CTRL_REQ.cmdReady`, poll until the TPM
   leaves Idle (`CTRL_STS.tpmIdle` clear) with no error and `cmdReady`
   self-cleared.
3. **Write command** into the data buffer (`common.WriteBytes`).
4. **Start** — `CTRL_START.start = 1`.
5. **Wait** for the TPM to clear `CTRL_START.start` (bounded spin); on
   expiry drive `CTRL_CANCEL` and return `ErrTimeout`.
6. **Check** `CTRL_STS.Error`.
7. **Read** the 10-byte response header for `responseSize`, bounds-check,
   then read the whole response.
8. **Release** — `CTRL_REQ.goIdle`.

## Endianness

The TPM 2.0 **wire** protocol (the command/response byte stream) is
**big-endian** and handled by `common`'s codec. The CRB **control
registers** are **little-endian** MMIO; `common.Regs.Read32/Write32`
access them in the platform's native width, so this package never
byte-swaps register values — it only moves the opaque command/response
byte streams through the data buffer.

## Conventions

Pure Go, `CGO_ENABLED=0`, no assembly, BSD-3-Clause, 100% statement
coverage (`GOWORK=off go test -cover`).

Register offsets, bit positions, and handshake steps are cited inline to
the TCG PTP specification. A few values that the spec leaves
implementation-defined (the data-buffer base offset, the `CTRL_CANCEL`
write value, the spin bound, the buffer size) are marked `// INFERRED:`
and verified by a validate harness against a real `swtpm` under QEMU
`-device tpm-crb`.
