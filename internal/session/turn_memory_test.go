package session

import (
	"context"
	"strings"
	"testing"
)

func TestTurnMemoryAdmissionRollback(t *testing.T) {
	for _, table := range []string{"turn_memory", "dispatch_records", "admission_keys"} {
		t.Run(table, func(t *testing.T) {
			s := queueStore(t)
			ctx := context.Background()
			task, _ := s.Create(ctx, "selected")
			rec, err := s.CreateMemory(ctx, MemoryScope{TaskID: task.ID}, MemoryFact{Content: "fact", SourceRef: "user:1", ApprovalRef: "user:2", Kind: "verified", Confidence: 1, Confidentiality: "private"})
			if err != nil {
				t.Fatal(err)
			}
			identity := AdmissionIdentity{Principal: "owner", Key: "key", Digest: strings.Repeat("a", 64)}
			if _, err = s.db.Exec(`CREATE TRIGGER fail_memory_admission BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
				t.Fatal(err)
			}
			accept := func() (Run, error) {
				return s.AcceptQueuedMemory(ctx, task.ID, task.Revision, queuePrompt("hello"), "", nil, 4, []MemorySelection{{ID: rec.ID, Revision: 1}}, 64000, MemoryDestination{ID: "destination"}, identity)
			}
			if _, err = accept(); err == nil {
				t.Fatal("injected failure succeeded")
			}
			for _, table := range []string{"turn_memory", "dispatch_records", "admission_keys", "runs", "messages"} {
				var count int
				if err = s.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
					t.Fatal(table, count, err)
				}
			}
			loaded, _ := s.Get(ctx, task.ID)
			if loaded.Revision != task.Revision {
				t.Fatal("revision did not roll back")
			}
			if _, err = s.db.Exec(`DROP TRIGGER fail_memory_admission`); err != nil {
				t.Fatal(err)
			}
			if _, err = accept(); err != nil {
				t.Fatal("rollback consumed admission", err)
			}
		})
	}
}
