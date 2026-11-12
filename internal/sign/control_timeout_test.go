package sign

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestControlQueueWaitHonorsDeadlineForSignAndO3(t *testing.T) {
	for _, operation := range []string{"sign", "prepare", "account", "o3"} {
		t.Run(operation, func(t *testing.T) {
			client := New("http://127.0.0.1:1/sign", 20*time.Millisecond)
			client.control.Lock()
			defer client.control.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				switch operation {
				case "sign":
					_, err = client.Sign(ctx, "heartbeat", 1, nil)
				case "prepare":
					err = client.Prepare(ctx, make([]byte, 16), 123456)
				case "account":
					err = client.Account(ctx, 123456, true)
				case "o3":
					_, err = client.DoO3(ctx, "GET", "/o3/pull", nil)
				}
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("wrong failure: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("mutex wait ignored deadline")
			}
		})
	}
}
