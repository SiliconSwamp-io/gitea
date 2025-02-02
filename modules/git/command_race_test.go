// Copyright 2017 The Gitea Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

//go:build race
// +build race

package git

import (
	"context"
	"testing"
	"time"
)

func TestRunWithContextNoTimeout(t *testing.T) {
	maxLoops := 10

	// 'git --version' does not block so it must be finished before the timeout triggered.
	cmd := NewCommand(context.Background(), "--version")
	for i := 0; i < maxLoops; i++ {
		if err := cmd.RunWithContext(&RunContext{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunWithContextTimeout(t *testing.T) {
	maxLoops := 10

	// 'git hash-object --stdin' blocks on stdin so we can have the timeout triggered.
	cmd := NewCommand(context.Background(), "hash-object", "--stdin")
	for i := 0; i < maxLoops; i++ {
		if err := cmd.RunWithContext(&RunContext{Timeout: 1 * time.Millisecond}); err != nil {
			if err != context.DeadlineExceeded {
				t.Fatalf("Testing %d/%d: %v", i, maxLoops, err)
			}
		}
	}
}
