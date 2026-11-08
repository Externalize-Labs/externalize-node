package archive

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// Ledger holds the raw XDR records an archive stores for one ledger, plus the
// two records of its successor that bind its header. Records are passed
// through byte for byte; nothing is re-encoded.
type Ledger struct {
	Sequence     uint32
	Header       []byte // LedgerHeaderHistoryEntry
	SCP          []byte // ScpHistoryEntry
	Results      []byte // TransactionHistoryResultEntry, nil if the ledger had none
	Transactions []byte // TransactionHistoryEntry, nil unless requested

	// Validators sign only a ledger's StellarValue, not its header. Ledger
	// N+1's externalized value commits to its transaction set, which carries
	// previousLedgerHash = SHA-256(header N), so these two records certify
	// the rest of the header. Always set.
	NextSCP          []byte // ScpHistoryEntry of ledger Sequence+1
	NextTransactions []byte // TransactionHistoryEntry of ledger Sequence+1
}

// Source returns archived ledgers.
type Source interface {
	Ledger(ctx context.Context, seq uint32, withTransactions bool) (*Ledger, error)
}

// ErrSuccessorNotPublished means the ledger itself is archived but it ends its
// checkpoint, and the next checkpoint, which holds the successor's records,
// is not published yet. It clears within one checkpoint interval (about six
// minutes).
var ErrSuccessorNotPublished = errors.New("the next ledger's checkpoint is not published yet")

type file struct {
	cat Category
	cp  uint32
}

// Ledger implements Source over a live archive. The checkpoint files of the
// ledger and of its successor (the same checkpoint unless seq ends one) are
// fetched concurrently.
func (c *Client) Ledger(ctx context.Context, seq uint32, withTransactions bool) (*Ledger, error) {
	if seq == 0 || seq == math.MaxUint32 {
		return nil, fmt.Errorf("ledger %d has no archived successor", seq)
	}
	cp, next := Checkpoint(seq), Checkpoint(seq+1)
	// The ledger's own files come first so their errors take precedence.
	files := []file{{Headers, cp}, {SCP, cp}, {Results, cp}}
	if withTransactions || next == cp {
		files = append(files, file{Transactions, cp})
	}
	if next != cp {
		files = append(files, file{SCP, next}, file{Transactions, next})
	}
	records := make(map[file][][]byte, len(files))
	errs := make([]error, len(files))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, f := range files {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs, err := c.records(ctx, f.cat, f.cp)
			mu.Lock()
			records[f], errs[i] = recs, err
			mu.Unlock()
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err == nil {
			continue
		}
		if files[i].cp != cp && errors.Is(err, ErrNotPublished) {
			return nil, fmt.Errorf("ledger %d: %w (checkpoint %d); retry in a few minutes", seq, ErrSuccessorNotPublished, next)
		}
		return nil, err
	}

	out := &Ledger{Sequence: seq}
	var err error
	if out.Header, err = find(records[file{Headers, cp}], seq, headerSeq); err != nil {
		return nil, fmt.Errorf("ledger header: %w", err)
	}
	if out.SCP, err = find(records[file{SCP, cp}], seq, scpSeq); err != nil {
		return nil, fmt.Errorf("scp messages: %w", err)
	}
	out.Results, _ = find(records[file{Results, cp}], seq, leadingSeq)
	if withTransactions {
		out.Transactions, _ = find(records[file{Transactions, cp}], seq, leadingSeq)
	}
	if out.NextSCP, err = find(records[file{SCP, next}], seq+1, scpSeq); err != nil {
		return nil, fmt.Errorf("next ledger's scp messages: %w", err)
	}
	if out.NextTransactions, err = find(records[file{Transactions, next}], seq+1, leadingSeq); err != nil {
		return nil, fmt.Errorf("next ledger's transaction set: %w", err)
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
