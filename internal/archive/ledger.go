package archive

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"

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

// Ledger implements Source over a live archive. The checkpoint's category
// files are fetched concurrently.
func (c *Client) Ledger(ctx context.Context, seq uint32, withTransactions bool) (*Ledger, error) {
	cp := Checkpoint(seq)
	cats := []Category{Headers, SCP, Results}
	if withTransactions {
		cats = append(cats, Transactions)
	}
	records := make([][][]byte, len(cats))
	errs := make([]error, len(cats))
	var wg sync.WaitGroup
	for i, cat := range cats {
		wg.Add(1)
		go func() {
			defer wg.Done()
			records[i], errs[i] = c.records(ctx, cat, cp)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	out := &Ledger{Sequence: seq}
	var err error
	if out.Header, err = find(records[0], seq, headerSeq); err != nil {
		return nil, fmt.Errorf("ledger header: %w", err)
	}
	if out.SCP, err = find(records[1], seq, scpSeq); err != nil {
		return nil, fmt.Errorf("scp messages: %w", err)
	}
	out.Results, _ = find(records[2], seq, leadingSeq)
	if withTransactions {
		out.Transactions, _ = find(records[3], seq, leadingSeq)
	}
	return out, nil
}

// records returns a checkpoint file's records, from memory when possible.
func (c *Client) records(ctx context.Context, cat Category, cp uint32) ([][]byte, error) {
	key := Path(cat, cp)
	if recs, ok := c.memory.get(key); ok {
		return recs, nil
	}
	raw, err := c.File(ctx, cat, cp)
	if err != nil {
		return nil, err
	}
	recs, err := Frames(raw)
	if err != nil {
		return nil, err
	}
	c.memory.put(key, recs)
	return recs, nil
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
