package pushed

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// The quota and the daily limit are checked in the transaction that stores: parallel uploads that each fit alone but not together
// store only what fits, and an item past the quota is refused.
func TestQuotaIsCheckedWhereTheRowIsWritten(t *testing.T) {
	s := open(t)
	ws := workspace.ID(1)
	q := Quota{Limit: 350}
	var wg sync.WaitGroup
	var mu sync.Mutex
	stored, refused := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := []byte(fmt.Sprintf("%0100d", i))
			sum := sha256.Sum256(body)
			_, err := s.PutAsset(ws, "ada", hex.EncodeToString(sum[:]), "image/png", body, q)
			var limit LimitError
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				stored++
			case errors.As(err, &limit) && limit.Quota:
				refused++
			default:
				t.Errorf("upload %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if stored != 3 || refused != 5 || s.AssetBytes(ws) != 300 {
		t.Fatalf("stored %d, refused %d, %d bytes", stored, refused, s.AssetBytes(ws))
	}
	it := item(t, lineA, strings.Repeat("x", 100))
	var limit LimitError
	if _, err := s.Put(ws, ada, it, nil, q); !errors.As(err, &limit) || !limit.Quota {
		t.Fatalf("an item past the quota: %v", err)
	}
	if _, err := s.Put(ws, ada, it, nil, Quota{}); err != nil {
		t.Fatalf("no quota: %v", err)
	}
}
