<p align="center"><img src="https://raw.githubusercontent.com/go-tpm2/brand/main/social/go-tpm2.png" alt="go-tpm2/crb" width="720"></p>

# go-tpm2/crb

[![CI](https://github.com/go-tpm2/crb/actions/workflows/ci.yml/badge.svg)](https://github.com/go-tpm2/crb/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-tpm2/crb.svg)](https://pkg.go.dev/github.com/go-tpm2/crb)
[![Coverage](https://img.shields.io/badge/coverage-100%25-brightgreen)](#conventions)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)

A pure-Go TPM 2.0 **Command Response Buffer (CRB)** MMIO transport. **v0.1.0.**

`crb` implements [`github.com/go-tpm2/common`](https://github.com/go-tpm2/common)'s
`Transport` interface over its `Regs` MMIO accessor. The platform owns the
register mapping (a physical MMIO window, an `mmap` of `/dev/mem`, or a test
stub) and exposes it through `common.Regs`; this package drives the CRB
register handshake defined by the TCG **PC Client Platform TPM Profile (PTP)
Specification**, clause *Command Response Buffer Interface*.

Sibling repos: [`common`](https://github.com/go-tpm2/common) (interfaces +
codec), [`tis`](https://github.com/go-tpm2/tis) (the FIFO transport
alternative), [`tpm2`](https://github.com/go-tpm2/tpm2) (the command layer
that rides on this `Transport`), and
[`validate`](https://github.com/go-tpm2/validate) (live swtpm validation).

## Install

```sh
go get github.com/go-tpm2/crb
```

## Usage

```go
import (
    "github.com/go-tpm2/common"
    "github.com/go-tpm2/crb"
)

// r is a platform-provided common.Regs over the CRB control area base.
dev, err := crb.Open(r)
if err != nil {
    // not a CRB interface, or the interface never reported valid
}

// *crb.CRB satisfies common.Transport, so it plugs straight into the
// go-tpm2/tpm2 command layer:
//   tpm := tpm2.New(dev)
//   tpm.Startup(uint16(common.SUClear))
//   rnd, _ := tpm.GetRandom(20)

// …or drive raw command buffers directly:
cmd := common.BuildCommand(uint16(common.TagNoSessions),
    uint32(common.CCGetRandom), []byte{0x00, 0x02})
rsp, err := dev.Send(cmd) // common.Transport.Send
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

Pure Go, `CGO_ENABLED=0`, no assembly, big-endian TPM wire (via `common`),
BSD-3-Clause, 100% statement coverage (`GOWORK=off go test -cover`),
`GOWORK=off`.

Register offsets, bit positions, and handshake steps are cited inline to
the TCG PTP specification. A few values that the spec leaves
implementation-defined (the data-buffer base offset, the `CTRL_CANCEL`
write value, the spin bound, the buffer size) are marked `// INFERRED:`
and verified by the [`validate`](https://github.com/go-tpm2/validate)
harness against a real `swtpm` 0.10.1 under QEMU `-device tpm-crb`.

## Specifications

- TCG PC Client Platform TPM Profile (**PTP**) — *Command Response Buffer Interface*.
- TCG TPM 2.0 Library, Parts 1–4 (wire format, via `common`).

## License

BSD-3-Clause. See [LICENSE](LICENSE).
