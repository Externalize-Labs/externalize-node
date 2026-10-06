package archive

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// Ledger holds the raw XDR records an archive stores for one ledger. Records
// are passed through byte for byte; nothing is re-encoded.
type Ledger struct {
	Sequence     uint32
	Header       []byte // LedgerHeaderHistoryEntry
	SCP          []byte // ScpHistoryEntry
	Results      []byte // TransactionHistoryResultEntry, nil if the ledger had none
	Transactions []byte // TransactionHistoryEntry, nil unless requested
}

// Source returns archived ledgers.
type Source interface {
	Ledger(ctx context.Context, seq uint32, withTransactions bool) (*Ledger, error)
}

// Ledger implements Source over a live archive.
func (c *Client) Ledger(ctx context.Context, seq uint32, withTransactions bool) (*Ledger, error) {
	cp := Checkpoint(seq)
	file := func(cat Category) ([][]byte, error) {
		raw, err := c.File(ctx, cat, cp)
		if err != nil {
			return nil, err
		}
		return Frames(raw)
	}
	out := &Ledger{Sequence: seq}

	headers, err := file(Headers)
	if err != nil {
		return nil, err
	}
	if out.Header, err = find(headers, seq, headerSeq); err != nil {
		return nil, fmt.Errorf("ledger header: %w", err)
	}

	scp, err := file(SCP)
	if err != nil {
		return nil, err
	}
	if out.SCP, err = find(scp, seq, scpSeq); err != nil {
		return nil, fmt.Errorf("scp messages: %w", err)
	}

	results, err := file(Results)
	if err != nil {
		return nil, err
	}
	out.Results, _ = find(results, seq, leadingSeq)

	if withTransactions {
		txs, err := file(Transactions)
		if err != nil {
			return nil, err
		}
		out.Transactions, _ = find(txs, seq, leadingSeq)
	}
	return out, nil
}

func find(records [][]byte, seq uint32, seqOf func([]byte) (uint32, error)) ([]byte, error) {
	for _, r := range records {
		s, err := seqOf(r)
		if err != nil {
			return nil, err
		}
		if s == seq {
			return r, nil
		}
	}
	return nil, fmt.Errorf("ledger %d not in checkpoint %d", seq, Checkpoint(seq))
}

func headerSeq(rec []byte) (uint32, error) {
	var e xdr.LedgerHeaderHistoryEntry
	if err := xdr.SafeUnmarshal(rec, &e); err != nil {
		return 0, err
	}
	return uint32(e.Header.LedgerSeq), nil
}

func scpSeq(rec []byte) (uint32, error) {
	var e xdr.ScpHistoryEntry
	if err := xdr.SafeUnmarshal(rec, &e); err != nil {
		return 0, err
	}
	if e.V0 == nil {
		return 0, fmt.Errorf("unsupported ScpHistoryEntry version %d", e.V)
	}
	return uint32(e.V0.LedgerMessages.LedgerSeq), nil
}

// leadingSeq reads the ledgerSeq that both TransactionHistoryEntry and
// TransactionHistoryResultEntry start with, without decoding the rest.
func leadingSeq(rec []byte) (uint32, error) {
	if len(rec) < 4 {
		return 0, fmt.Errorf("record too short")
	}
	return binary.BigEndian.Uint32(rec), nil
}
