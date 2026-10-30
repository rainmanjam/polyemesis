//go:build !e2echat

package main

import (
	"context"
	"log/slog"

	"github.com/rainmanjam/polyemesis/internal/chat"
)

// startE2EChat is a no-op in every release build. The e2echat build tag swaps
// in e2echat_on.go, which attaches fake chat adapters for the Docker end-to-end
// test of the chat overlay; nothing of it is compiled into a shipped binary.
func startE2EChat(context.Context, *chat.Hub, *slog.Logger) {}
