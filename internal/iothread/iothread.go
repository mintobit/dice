// Copyright (c) 2022-present, DiceDB contributors
// All rights reserved. Licensed under the BSD 3-Clause License. See LICENSE file in the project root for full license information.

package iothread

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/dicedb/dice/internal/auth"
	"github.com/dicedb/dice/internal/clientio/iohandler"
	"github.com/dicedb/dice/internal/cmd"
)

// IOThread interface
type IOThread interface {
	ID() string
	Start(context.Context) error
	Stop() error
}

type BaseIOThread struct {
	IOThread
	id                string
	ioHandler         iohandler.IOHandler
	Session           *auth.Session
	ioThreadReadChan  chan []byte      // Channel to send data to the command handler
	ioThreadWriteChan chan interface{} // Channel to receive data from the command handler
	ioThreadErrChan   chan error       // Channel to receive errors from the ioHandler
}

func NewIOThread(id string, ioHandler iohandler.IOHandler,
	ioThreadReadChan chan []byte, ioThreadWriteChan chan interface{},
	ioThreadErrChan chan error) *BaseIOThread {
	return &BaseIOThread{
		id:                id,
		ioHandler:         ioHandler,
		Session:           auth.NewSession(),
		ioThreadReadChan:  ioThreadReadChan,
		ioThreadWriteChan: ioThreadWriteChan,
		ioThreadErrChan:   ioThreadErrChan,
	}
}

func (t *BaseIOThread) ID() string {
	return t.id
}

func (t *BaseIOThread) Start(ctx context.Context) error {
	slog.Debug("starting io thread", slog.Int64("time_ms", time.Now().UnixMilli()))
	// local channels to communicate between Start and startInputReader goroutine
	incomingDataChan := make(chan []byte) // data channel
	readErrChan := make(chan error)       // error channel

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	// This method is run in a separate goroutine to ensure that the main event loop in the Start method
	// remains non-blocking and responsive to other events, such as adhoc requests or context cancellations.
	go t.startInputReader(runCtx, incomingDataChan, readErrChan)

	for {
		select {
		case <-ctx.Done():
			if err := t.Stop(); err != nil {
				slog.Warn("Error stopping io-thread:", slog.String("id", t.id), slog.Any("error", err))
			}
			return ctx.Err()
		case data := <-incomingDataChan:
			t.ioThreadReadChan <- data
		case err := <-readErrChan:
			slog.Debug("Read error in io-thread, connection closed possibly", slog.String("id", t.id), slog.Any("error", err))
			t.ioThreadErrChan <- err
			return err
		case resp := <-t.ioThreadWriteChan:
			err := t.ioHandler.Write(ctx, resp)
			if err != nil {
				slog.Debug("error while sending response to the client", slog.String("id", t.id), slog.Any("error", err))
				continue
			}
			slog.Debug("wrote response to client",
				slog.Any("resp", resp),
				slog.Int64("time_ms", time.Now().UnixMilli()))
		}
	}
}

func (t *BaseIOThread) StartSync(_ context.Context, execute func(c *cmd.Cmd) (*cmd.CmdRes, error)) error {
	slog.Debug("starting sync io thread", slog.Int64("time_ms", time.Now().UnixMilli()))
	c, err := t.ioHandler.ReadSync()
	if err != nil {
		return err
	}
	res, err := execute(c)
	fmt.Println(res.R.Msg, err)
	return nil
}

// startInputReader continuously reads input data from the ioHandler and sends it to the incomingDataChan.
func (t *BaseIOThread) startInputReader(ctx context.Context, incomingDataChan chan []byte, readErrChan chan error) {
	defer close(incomingDataChan)
	defer close(readErrChan)

	for {
		data, err := t.ioHandler.Read(ctx)
		if err != nil {
			select {
			case readErrChan <- err:
			case <-ctx.Done():
			}
			return
		}

		select {
		case incomingDataChan <- data:
		case <-ctx.Done():
			return
		}
	}
}

func (t *BaseIOThread) Stop() error {
	slog.Debug("stopping io thread", slog.String("id", t.id))
	t.Session.Expire()
	return nil
}
