// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, the go-tpm2/crb authors. All rights reserved.

// Package crb implements the TPM 2.0 Command Response Buffer (CRB)
// interface as a github.com/go-tpm2/common.Transport over the
// platform-provided common.Regs MMIO accessor.
//
// The CRB interface and its register block are defined by the TCG "PC
// Client Platform TPM Profile (PTP) Specification" (Family 2.0), in the
// clause "Command Response Buffer Interface" and its register tables
// ("Register Space" / "CRB Interface Definition"). Unless noted with an
// INFERRED marker, every register offset, bit position, and handshake
// step below is taken from that specification.
//
// Endianness note: the TPM 2.0 *wire* protocol (the command/response
// byte stream this transport carries) is BIG-ENDIAN and is encoded by
// github.com/go-tpm2/common's codec. The CRB *control registers*
// themselves are LITTLE-ENDIAN MMIO; common.Regs.Read32/Write32 access
// them in the platform's native register width, so this package never
// byte-swaps register values — it only ever moves the opaque command and
// response byte streams through the data buffer with common.WriteBytes /
// common.ReadBytes.
//
// Conventions: pure Go, CGO_ENABLED=0, no architecture-specific
// assembly, BSD-3-Clause on every file, 100% statement coverage, and
// GOWORK=off.
package crb

import (
	"github.com/go-tpm2/common"
)

// Register offsets within a single CRB locality's control area, in bytes
// from the locality (control-area) base.
//
// TCG PTP, clause "Command Response Buffer Interface", table "Register
// Space (CRB Interface)". The layout below is the locality control area
// (one per locality); this driver operates a single locality.
const (
	// regLocState (LOC_STATE) — read-only locality state. TCG PTP,
	// "TPM_LOC_STATE_x".
	regLocState = 0x00
	// regLocCtrl (LOC_CTRL) — write-only locality control
	// (requestAccess / relinquish / seize / resetEstablishment). TCG
	// PTP, "TPM_LOC_CTRL_x".
	regLocCtrl = 0x08
	// regLocSts (LOC_STS) — locality status (granted / beenSeized).
	// TCG PTP, "TPM_LOC_STS_x".
	regLocSts = 0x0C
	// regInterfaceID (INTERFACE_ID) — interface identification and
	// capabilities; its InterfaceType nibble selects CRB vs FIFO/TIS.
	// TCG PTP, "TPM_INTERFACE_ID_x".
	regInterfaceID = 0x30
	// regCtrlExt (CTRL_EXT) — control area extension (reserved /
	// vendor). TCG PTP, "CRB_CTRL_EXT_x".
	regCtrlExt = 0x38
	// regCtrlReq (CTRL_REQ) — control request (cmdReady / goIdle).
	// TCG PTP, "CRB_CTRL_REQ_x".
	regCtrlReq = 0x40
	// regCtrlSts (CTRL_STS) — control status (tpmIdle / tpmSts error).
	// TCG PTP, "CRB_CTRL_STS_x".
	regCtrlSts = 0x44
	// regCtrlCancel (CTRL_CANCEL) — command cancellation. TCG PTP,
	// "CRB_CTRL_CANCEL_x".
	regCtrlCancel = 0x48
	// regCtrlStart (CTRL_START) — command start / completion. TCG PTP,
	// "CRB_CTRL_START_x".
	regCtrlStart = 0x4C
	// regIntEnable (INT_ENABLE) — interrupt enable. TCG PTP,
	// "CRB_INT_ENABLE_x".
	regIntEnable = 0x50
	// regIntSts (INT_STS) — interrupt status. TCG PTP,
	// "CRB_INT_STS_x".
	regIntSts = 0x54
	// regCtrlCmdSize (CTRL_CMD_SIZE) — command buffer size. TCG PTP,
	// "CRB_CTRL_CMD_SIZE_x".
	regCtrlCmdSize = 0x58
	// regCtrlCmdLAddr (CTRL_CMD_LADDR) — command buffer low address.
	// TCG PTP, "CRB_CTRL_CMD_LADDR_x".
	regCtrlCmdLAddr = 0x5C
	// regCtrlCmdHAddr (CTRL_CMD_HADDR) — command buffer high address.
	// TCG PTP, "CRB_CTRL_CMD_HADDR_x".
	regCtrlCmdHAddr = 0x60
	// regCtrlRspSize (CTRL_RSP_SIZE) — response buffer size. TCG PTP,
	// "CRB_CTRL_RSP_SIZE_x".
	regCtrlRspSize = 0x64
	// regCtrlRspAddr (CTRL_RSP_ADDR) — response buffer address (64-bit;
	// low dword at this offset). TCG PTP, "CRB_CTRL_RSP_ADDR_x".
	regCtrlRspAddr = 0x68

	// regData is the start of the command/response DATA buffer. The PTP
	// permits the command and response buffers to be placed anywhere the
	// CTRL_CMD_*ADDR / CTRL_RSP_ADDR registers point; the conventional
	// layout used by QEMU's tpm-crb and most firmware places a single
	// shared buffer immediately after the control area at offset 0x80.
	//
	// INFERRED: that the data buffer base is 0x80 and that the command
	// and response share it. Confirm against a real swtpm under QEMU
	// -device tpm-crb by reading CTRL_CMD_LADDR/HADDR and CTRL_RSP_ADDR
	// and subtracting the control-area physical base; the validate
	// harness does exactly this.
	regData = 0x80
)

// LOC_STATE (regLocState) bit definitions. TCG PTP, "TPM_LOC_STATE_x".
const (
	// locStateEstablishment — tpmEstablished (bit 0). Not used by the
	// send path but defined for completeness. TCG PTP, "TPM_LOC_STATE_x"
	// field "tpmEstablished".
	locStateEstablishment = 1 << 0
	// locStateLocAssigned — locAssigned (bit 1): a locality currently
	// owns the interface. TCG PTP, "TPM_LOC_STATE_x" field
	// "locAssigned".
	locStateLocAssigned = 1 << 1
	// locStateActiveShift / locStateActiveMask — activeLocality, the
	// 3-bit field (bits 4..2) naming which locality is assigned when
	// locAssigned is set. TCG PTP, "TPM_LOC_STATE_x" field
	// "activeLocality".
	locStateActiveShift = 2
	locStateActiveMask  = 0x7 << locStateActiveShift
	// locStateValid — tpmRegValidSts (bit 7): the register contents are
	// valid. Software waits for this before trusting LOC_STATE. TCG PTP,
	// "TPM_LOC_STATE_x" field "tpmRegValidSts".
	locStateValid = 1 << 7
)

// LOC_CTRL (regLocCtrl) bit definitions. TCG PTP, "TPM_LOC_CTRL_x".
const (
	// locCtrlRequestAccess — requestAccess (bit 0): request that this
	// locality be granted the interface. TCG PTP, "TPM_LOC_CTRL_x"
	// field "requestAccess".
	locCtrlRequestAccess = 1 << 0
	// locCtrlRelinquish — relinquish (bit 1): give up the locality.
	// TCG PTP, "TPM_LOC_CTRL_x" field "relinquish".
	locCtrlRelinquish = 1 << 1
)

// LOC_STS (regLocSts) bit definitions. TCG PTP, "TPM_LOC_STS_x".
const (
	// locStsGranted — granted (bit 0): the requested locality has been
	// granted. TCG PTP, "TPM_LOC_STS_x" field "Granted".
	locStsGranted = 1 << 0
)

// CTRL_REQ (regCtrlReq) bit definitions. TCG PTP, "CRB_CTRL_REQ_x".
const (
	// ctrlReqCmdReady — cmdReady (bit 0): request transition to the
	// Ready (command-reception) state. TCG PTP, "CRB_CTRL_REQ_x" field
	// "cmdReady".
	ctrlReqCmdReady = 1 << 0
	// ctrlReqGoIdle — goIdle (bit 1): request transition to the Idle
	// state, releasing the interface after a command. TCG PTP,
	// "CRB_CTRL_REQ_x" field "goIdle".
	ctrlReqGoIdle = 1 << 1
)

// CTRL_STS (regCtrlSts) bit definitions. TCG PTP, "CRB_CTRL_STS_x".
const (
	// ctrlStsError — tpmSts / Error (bit 0): the TPM has encountered a
	// fatal error and the interface must be reset. TCG PTP,
	// "CRB_CTRL_STS_x" field "tpmSts" (Error).
	ctrlStsError = 1 << 0
	// ctrlStsIdle — tpmIdle (bit 1): the TPM is in the Idle state. TCG
	// PTP, "CRB_CTRL_STS_x" field "tpmIdle".
	ctrlStsIdle = 1 << 1
)

// CTRL_START (regCtrlStart) bit definitions. TCG PTP, "CRB_CTRL_START_x".
const (
	// ctrlStart — start (bit 0): software sets it to hand the command
	// in the buffer to the TPM; the TPM clears it when the response is
	// ready. TCG PTP, "CRB_CTRL_START_x" field "start".
	ctrlStart = 1 << 0
)

// CTRL_CANCEL (regCtrlCancel) bit definitions. TCG PTP,
// "CRB_CTRL_CANCEL_x".
const (
	// ctrlCancelSet — the value written to request cancellation of the
	// command in progress. TCG PTP, "CRB_CTRL_CANCEL_x" field "cancel"
	// describes a non-zero write as the cancel request and a zero write
	// as clearing it.
	//
	// INFERRED: that the specific value 0x00000001 (rather than any
	// other non-zero pattern) is what firmware and QEMU's tpm-crb
	// expect. Confirm by writing 0x1 to CTRL_CANCEL against a real
	// swtpm under QEMU -device tpm-crb and observing the in-flight
	// command return TPM_RC_CANCELED.
	ctrlCancelSet   = 0x00000001
	ctrlCancelClear = 0x00000000
)

// INTERFACE_ID (regInterfaceID) field definitions. TCG PTP,
// "TPM_INTERFACE_ID_x".
const (
	// ifaceTypeMask — InterfaceType occupies the low nibble (bits 3..0).
	// TCG PTP, "TPM_INTERFACE_ID_x" field "InterfaceType".
	ifaceTypeMask = 0x0F
	// ifaceTypeCRB — InterfaceType value 1 selects the CRB interface
	// (value 0 selects FIFO/TIS). TCG PTP, "TPM_INTERFACE_ID_x" field
	// "InterfaceType", enumeration.
	ifaceTypeCRB = 0x1
)

// maxSpin bounds every CRB polling loop. The PTP defines the handshakes
// by state, not by a wall-clock duration, and a register-level driver
// has no portable timer; this counter caps the busy-wait so a wedged or
// absent TPM yields ErrTimeout instead of hanging. It is deliberately
// large enough to clear any real interface transition.
//
// INFERRED: the exact bound. A timer-based deadline keyed to the PTP's
// recommended command-duration timeouts (TIMEOUT_B / TIMEOUT_C class)
// would be more faithful; the validate harness against real swtpm
// confirms the chosen bound never trips on a healthy TPM.
//
// It is a package var rather than a const only so the test suite can
// shrink it to exercise the timeout branches quickly; production code
// never reassigns it.
var maxSpin = 1 << 24

// Error sentinels for the CRB transport, typed as common.Error so callers
// may compare with ==.
const (
	// ErrNotCRB is returned by Open when INTERFACE_ID does not advertise
	// the CRB interface type.
	ErrNotCRB = common.Error("crb: interface is not CRB")
	// ErrLocality is returned when the locality cannot be granted.
	ErrLocality = common.Error("crb: locality not granted")
	// ErrNotReady is returned when the TPM never reaches command-ready.
	ErrNotReady = common.Error("crb: command-ready not granted")
	// ErrTPM is returned when CTRL_STS reports a fatal TPM error.
	ErrTPM = common.Error("crb: TPM reported a fatal error")
	// ErrTimeout is returned when the TPM never clears CTRL_START.start.
	ErrTimeout = common.Error("crb: timed out waiting for command completion")
	// ErrShortResponse is returned when the response is smaller than a
	// TPM 2.0 header.
	ErrShortResponse = common.Error("crb: response shorter than header")
	// ErrResponseTooLarge is returned when the declared response size
	// exceeds the data buffer.
	ErrResponseTooLarge = common.Error("crb: response larger than data buffer")
)

// bufSize is the number of bytes of the shared command/response data
// buffer this driver is willing to use, and the ceiling it enforces on a
// response's declared size.
//
// CONFIRMED-AND-CORRECTED by the go-tpm2/validate harness against a real
// swtpm under QEMU 10.2 `-device tpm-crb`: the data buffer is NOT a full
// 4 KiB. QEMU lays out the locality as a 0x80-byte control area
// (fed40000..fed4007f) immediately followed by the command/response buffer
// (fed40080..fed40fff), so the usable buffer is 0x1000 - 0x80 = 3968 bytes,
// and CTRL_CMD_SIZE / CTRL_RSP_SIZE both read back exactly 3968. The earlier
// INFERRED value of 4096 was the full locality size, not the buffer; using it
// as the response ceiling would wrongly admit a declared response of up to
// 4096 bytes and read 128 bytes past the end of the real buffer. 3968 is the
// value the hardware reports and the data buffer actually spans.
const bufSize = 3968

// CRB is a TPM 2.0 Command Response Buffer transport bound to a single
// locality's control area, reached through a common.Regs accessor. It
// satisfies common.Transport.
type CRB struct {
	r common.Regs
}

// compile-time assertion that *CRB satisfies common.Transport.
var _ common.Transport = (*CRB)(nil)

// Open binds a CRB transport to the register window r and validates that
// the window actually exposes a CRB interface.
//
// Validation follows TCG PTP: it reads INTERFACE_ID and checks that its
// InterfaceType nibble names the CRB interface ("TPM_INTERFACE_ID_x",
// field "InterfaceType"), then reads LOC_STATE and waits for
// tpmRegValidSts ("TPM_LOC_STATE_x", field "tpmRegValidSts") so the
// state register may be trusted by the caller.
func Open(r common.Regs) (*CRB, error) {
	id := r.Read32(regInterfaceID)
	if id&ifaceTypeMask != ifaceTypeCRB {
		return nil, ErrNotCRB
	}
	// Wait for LOC_STATE to report valid before returning. TCG PTP,
	// "TPM_LOC_STATE_x" field "tpmRegValidSts".
	for spin := 0; spin < maxSpin; spin++ {
		if r.Read32(regLocState)&locStateValid != 0 {
			return &CRB{r: r}, nil
		}
	}
	return nil, ErrTimeout
}

// Send transmits one fully-marshaled TPM 2.0 command buffer through the
// CRB interface and returns the full response buffer (header + params).
// It satisfies common.Transport.
//
// State machine (TCG PTP, clause "Command Response Buffer Interface",
// "CRB Interface State Transitions" and the per-register field tables):
//
//  1. Request the locality: write requestAccess to LOC_CTRL, then wait
//     for LOC_STS.Granted with LOC_STATE.locAssigned. (TCG PTP,
//     "TPM_LOC_CTRL_x"/"TPM_LOC_STS_x"/"TPM_LOC_STATE_x".)
//  2. Request command-ready: write cmdReady to CTRL_REQ and poll until
//     the TPM leaves Idle and is not in error — CTRL_STS with tpmIdle
//     clear and Error clear, and CTRL_REQ.cmdReady self-clearing once the
//     transition completes. (TCG PTP, "CRB_CTRL_REQ_x"/"CRB_CTRL_STS_x".)
//  3. Write the command into the data buffer (common.WriteBytes).
//  4. Set CTRL_START.start to hand the command to the TPM. (TCG PTP,
//     "CRB_CTRL_START_x".)
//  5. Poll CTRL_START.start until the TPM clears it (command complete),
//     bounded by maxSpin; on expiry write CTRL_CANCEL and return
//     ErrTimeout. (TCG PTP, "CRB_CTRL_START_x"/"CRB_CTRL_CANCEL_x".)
//  6. Check CTRL_STS for the Error bit. (TCG PTP, "CRB_CTRL_STS_x".)
//  7. Read the 10-byte response header to learn responseSize, validate
//     it against the buffer, then read the whole response.
//  8. Release the interface: write goIdle to CTRL_REQ. (TCG PTP,
//     "CRB_CTRL_REQ_x" field "goIdle".)
func (c *CRB) Send(cmd []byte) (rsp []byte, err error) {
	if err := c.requestLocality(); err != nil {
		return nil, err
	}
	// Whatever happens after the locality is granted, relinquish it on
	// the way out so a later Send starts clean.
	defer c.r.Write32(regLocCtrl, locCtrlRelinquish)

	if err := c.requestReady(); err != nil {
		return nil, err
	}

	// Write the command stream into the data buffer and hand it off.
	common.WriteBytes(c.r, regData, cmd)
	c.r.Write32(regCtrlStart, ctrlStart)

	if err := c.waitComplete(); err != nil {
		return nil, err
	}

	// A fatal TPM error after completion. TCG PTP, "CRB_CTRL_STS_x"
	// field "tpmSts" (Error).
	if c.r.Read32(regCtrlSts)&ctrlStsError != 0 {
		c.r.Write32(regCtrlReq, ctrlReqGoIdle)
		return nil, ErrTPM
	}

	rsp, err = c.readResponse()
	// Release the interface to Idle regardless of read outcome. TCG PTP,
	// "CRB_CTRL_REQ_x" field "goIdle".
	c.r.Write32(regCtrlReq, ctrlReqGoIdle)
	if err != nil {
		return nil, err
	}
	return rsp, nil
}

// requestLocality performs step 1 of Send: request and confirm locality
// ownership. TCG PTP, "TPM_LOC_CTRL_x"/"TPM_LOC_STS_x"/"TPM_LOC_STATE_x".
func (c *CRB) requestLocality() error {
	c.r.Write32(regLocCtrl, locCtrlRequestAccess)
	for spin := 0; spin < maxSpin; spin++ {
		if c.r.Read32(regLocSts)&locStsGranted != 0 &&
			c.r.Read32(regLocState)&locStateLocAssigned != 0 {
			return nil
		}
	}
	return ErrLocality
}

// requestReady performs step 2 of Send: drive the TPM to the Ready state
// so it will accept a command. TCG PTP, "CRB_CTRL_REQ_x"/"CRB_CTRL_STS_x".
func (c *CRB) requestReady() error {
	c.r.Write32(regCtrlReq, ctrlReqCmdReady)
	for spin := 0; spin < maxSpin; spin++ {
		sts := c.r.Read32(regCtrlSts)
		if sts&ctrlStsError != 0 {
			return ErrTPM
		}
		// Ready is reached when the TPM has left Idle and the cmdReady
		// request bit has self-cleared. TCG PTP, "CRB_CTRL_REQ_x" field
		// "cmdReady" (write-1, cleared by the TPM on completion) and
		// "CRB_CTRL_STS_x" field "tpmIdle".
		if sts&ctrlStsIdle == 0 &&
			c.r.Read32(regCtrlReq)&ctrlReqCmdReady == 0 {
			return nil
		}
	}
	return ErrNotReady
}

// waitComplete performs step 5 of Send: spin until the TPM clears
// CTRL_START.start, cancelling and reporting a timeout if it never does.
// TCG PTP, "CRB_CTRL_START_x"/"CRB_CTRL_CANCEL_x".
func (c *CRB) waitComplete() error {
	for spin := 0; spin < maxSpin; spin++ {
		if c.r.Read32(regCtrlStart)&ctrlStart == 0 {
			return nil
		}
	}
	// Timed out: request cancellation, then clear the cancel request,
	// then release to Idle. TCG PTP, "CRB_CTRL_CANCEL_x".
	c.r.Write32(regCtrlCancel, ctrlCancelSet)
	c.r.Write32(regCtrlCancel, ctrlCancelClear)
	c.r.Write32(regCtrlReq, ctrlReqGoIdle)
	return ErrTimeout
}

// readResponse performs step 7 of Send: read the header to learn the
// response size, bounds-check it, and read the whole response out of the
// data buffer. TCG "TPM 2.0 Part 1: Architecture", response header;
// buffer extents per TCG PTP "CRB_CTRL_RSP_SIZE_x".
func (c *CRB) readResponse() ([]byte, error) {
	hdr := make([]byte, common.HeaderSize)
	common.ReadBytes(c.r, regData, hdr)

	// responseSize is the u32 at offset 2 of the header. TCG "TPM 2.0
	// Part 1: Architecture", response header layout.
	size, _ := common.GetU32(hdr, 2)
	if size < common.HeaderSize {
		return nil, ErrShortResponse
	}
	if size > bufSize {
		return nil, ErrResponseTooLarge
	}

	rsp := make([]byte, size)
	copy(rsp, hdr)
	common.ReadBytes(c.r, regData+common.HeaderSize, rsp[common.HeaderSize:])
	return rsp, nil
}
