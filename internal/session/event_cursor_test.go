package session

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestEventIDsBoundedIsolatedAndDurable(t *testing.T) {
	s, path := openTest(t)
	ctx := context.Background()
	a, _ := s.Create(ctx, "A")
	b, _ := s.Create(ctx, "B")
	ar, _ := s.CreateRun(ctx, a.ID)
	br, _ := s.CreateRun(ctx, b.ID)
	want := []int64{}
	for i := 0; i < MaxEventPage+3; i++ {
		ev, err := s.AppendEvent(ctx, a.ID, ar.ID, "message", []byte(`{"private":"payload"}`))
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, ev.ID)
		if _, err := s.AppendEvent(ctx, b.ID, br.ID, "message", []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.EventIDs(ctx, a.ID, 0, MaxEventPage)
	if err != nil || !reflect.DeepEqual(page, want[:MaxEventPage]) {
		t.Fatalf("page: %v %v", page, err)
	}
	cursor := page[len(page)-1]
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	page, err = s.EventIDs(ctx, a.ID, cursor, MaxEventPage)
	if err != nil || !reflect.DeepEqual(page, want[MaxEventPage:]) {
		t.Fatalf("replay: %v %v", page, err)
	}
	if future, err := s.EventIDs(ctx, a.ID, want[len(want)-1]+1000, MaxEventPage); err != nil || len(future) != 0 {
		t.Fatalf("future cursor: %v %v", future, err)
	}
	if _, err = s.EventIDs(ctx, "absent", 0, 1); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	for _, args := range [][2]int64{{-1, 1}, {0, 0}, {0, MaxEventPage + 1}} {
		if _, err = s.EventIDs(ctx, a.ID, args[0], int(args[1])); err == nil {
			t.Fatal("accepted invalid bound")
		}
	}
}
