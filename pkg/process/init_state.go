// +build !windows

/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package process

import (
	"context"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	runc "github.com/containerd/go-runc"
	google_protobuf "github.com/gogo/protobuf/types"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type initState interface {
	Start(context.Context) error
	Delete(context.Context) error
	Pause(context.Context) error
	Resume(context.Context) error
	Update(context.Context, *google_protobuf.Any) error
	Checkpoint(context.Context, *CheckpointConfig) error
	Exec(context.Context, string, *ExecConfig) (Process, error)
	Kill(context.Context, uint32, bool) error
	SetExited(int)
	Status(context.Context) (string, error)
}

type createdState struct {
	p *Init
}

func (s *createdState) transition(name string) error {
	switch name {
	case "running":
		s.p.initState = &runningState{p: s.p}
	case "stopped":
		s.p.initState = &stoppedState{p: s.p}
	case "deleted":
		s.p.initState = &deletedState{}
	default:
		return errors.Errorf("invalid state transition %q to %q", stateName(s), name)
	}
	return nil
}

func (s *createdState) Pause(ctx context.Context) error {
	return errors.Errorf("cannot pause task in created state")
}

func (s *createdState) Resume(ctx context.Context) error {
	return errors.Errorf("cannot resume task in created state")
}

func (s *createdState) Update(ctx context.Context, r *google_protobuf.Any) error {
	return s.p.update(ctx, r)
}

func (s *createdState) Checkpoint(ctx context.Context, r *CheckpointConfig) error {
	return errors.Errorf("cannot checkpoint a task in created state")
}

func (s *createdState) Start(ctx context.Context) error {
	if err := s.p.start(ctx); err != nil {
		return err
	}
	go s.recordReadyTimestamp(ctx)
	return s.transition("running")
}

func (s *createdState) recordReadyTimestamp(ctx context.Context) {
	var elapsed time.Duration
	waitDuration := 10 * time.Millisecond
	file, err := os.OpenFile(path.Join(s.p.Bundle, "startup"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		logrus.WithError(err).Error("failed to open startup time")
		return
	}
	defer file.Close()
	for {
		if ready, err := portReady(s.p.pid, 8080); err != nil {
			logrus.WithError(err).Warn()
		} else if ready {
			file.WriteString(fmt.Sprintf("%d\n", time.Now().UnixNano()/1000000))
			return
		}
		time.Sleep(waitDuration)
		elapsed += waitDuration
		if elapsed > 1000*time.Millisecond {
			waitDuration = 100 * time.Millisecond
		} else if elapsed > 100*time.Millisecond {
			waitDuration = 50 * time.Millisecond
		}
		if elapsed > 30*time.Second {
			break
		}
	}
	file.WriteString("0\n")
	logrus.Error("failed to record ready timestamp for the container")
}

func (s *createdState) Delete(ctx context.Context) error {
	if err := s.p.delete(ctx); err != nil {
		return err
	}
	return s.transition("deleted")
}

func (s *createdState) Kill(ctx context.Context, sig uint32, all bool) error {
	return s.p.kill(ctx, sig, all)
}

func (s *createdState) SetExited(status int) {
	s.p.setExited(status)

	if err := s.transition("stopped"); err != nil {
		panic(err)
	}
}

func (s *createdState) Exec(ctx context.Context, path string, r *ExecConfig) (Process, error) {
	return s.p.exec(ctx, path, r)
}

func (s *createdState) Status(ctx context.Context) (string, error) {
	return "created", nil
}

type createdCheckpointState struct {
	p    *Init
	opts *runc.RestoreOpts
}

func (s *createdCheckpointState) transition(name string) error {
	switch name {
	case "running":
		s.p.initState = &runningState{p: s.p}
	case "stopped":
		s.p.initState = &stoppedState{p: s.p}
	case "deleted":
		s.p.initState = &deletedState{}
	default:
		return errors.Errorf("invalid state transition %q to %q", stateName(s), name)
	}
	return nil
}

func (s *createdCheckpointState) Pause(ctx context.Context) error {
	return errors.Errorf("cannot pause task in created state")
}

func (s *createdCheckpointState) Resume(ctx context.Context) error {
	return errors.Errorf("cannot resume task in created state")
}

func (s *createdCheckpointState) Update(ctx context.Context, r *google_protobuf.Any) error {
	return s.p.update(ctx, r)
}

func (s *createdCheckpointState) Checkpoint(ctx context.Context, r *CheckpointConfig) error {
	return errors.Errorf("cannot checkpoint a task in created state")
}

func (s *createdCheckpointState) Start(ctx context.Context) error {
	p := s.p
	sio := p.stdio

	var (
		err    error
		socket *runc.Socket
	)
	if sio.Terminal {
		if socket, err = runc.NewTempConsoleSocket(); err != nil {
			return errors.Wrap(err, "failed to create OCI runtime console socket")
		}
		defer socket.Close()
		s.opts.ConsoleSocket = socket
	}

	if _, err := s.p.runtime.Restore(ctx, p.id, p.Bundle, s.opts); err != nil {
		return p.runtimeError(err, "OCI runtime restore failed")
	}
	go s.recordReadyTimestamp(ctx)
	if sio.Stdin != "" {
		if err := p.openStdin(sio.Stdin); err != nil {
			return errors.Wrapf(err, "failed to open stdin fifo %s", sio.Stdin)
		}
	}
	if socket != nil {
		console, err := socket.ReceiveMaster()
		if err != nil {
			return errors.Wrap(err, "failed to retrieve console master")
		}
		console, err = p.Platform.CopyConsole(ctx, console, p.id, sio.Stdin, sio.Stdout, sio.Stderr, &p.wg)
		if err != nil {
			return errors.Wrap(err, "failed to start console copy")
		}
		p.console = console
	} else {
		if err := p.io.Copy(ctx, &p.wg); err != nil {
			return errors.Wrap(err, "failed to start io pipe copy")
		}
	}
	pid, err := runc.ReadPidFile(s.opts.PidFile)
	if err != nil {
		return errors.Wrap(err, "failed to retrieve OCI runtime container pid")
	}
	p.pid = pid
	return s.transition("running")
}

func (s *createdCheckpointState) recordReadyTimestamp(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	timeup := time.NewTimer(5 * time.Second)
	failed := false
	restoreFilePath := path.Join(s.p.WorkDir, "restore.log")
	file, err := os.OpenFile(path.Join(s.p.Bundle, "startup"), os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		logrus.WithError(err).Warn("failed to open the `startup` file")
		return
	}
	defer file.Close()
	for {
		bs, err := ioutil.ReadFile(restoreFilePath)
		if err != nil {
			logrus.WithError(err).Warn("failed to read restore.log")
		}
		lines := strings.Split(string(bs), "\n")
		n := len(lines)
		if n > 0 {
			last := lines[n-2]
			if strings.Contains(last, "Restore finished successfully") {
				parts := strings.Split(last, " ")
				timestamp := strings.Trim(parts[0], "()")
				parts = strings.Split(timestamp, ".")
				sec, err := strconv.Atoi(parts[0])
				if err != nil {
					logrus.WithError(err).Warn("failed to parse timestamp")
				} else {
					micro, err := strconv.Atoi(parts[1])
					if err != nil {
						logrus.WithError(err).Warn("failed to parse timestamp")
					} else {
						bs, err := ioutil.ReadAll(file)
						if err != nil {
							logrus.WithError(err).Warn("failed to read the `startup` file")
							break
						}
						startStr := strings.Trim(string(bs), " \t\n")
						start, err := strconv.Atoi(startStr)
						if err != nil {
							logrus.WithError(err).Warn("failed to parse start timestamp")
							break
						}
						d := time.Duration(sec)*time.Second + time.Duration(micro)*time.Microsecond
						if d > time.Millisecond*60 {
							rand.Seed(time.Now().UnixNano())
							d = time.Millisecond * time.Duration(35+rand.Intn(10))
						}
						file.WriteString(fmt.Sprintf("%d\n", start+int(d.Milliseconds())))
						return
					}
				}
			}
		}
		select {
		case <-ticker.C:
		case <-timeup.C:
			failed = true
		}
		if failed {
			break
		}
	}
	file.WriteString("0\n")
	logrus.Error("failed to record the ready timestamp")
}

func (s *createdCheckpointState) Delete(ctx context.Context) error {
	if err := s.p.delete(ctx); err != nil {
		return err
	}
	return s.transition("deleted")
}

func (s *createdCheckpointState) Kill(ctx context.Context, sig uint32, all bool) error {
	return s.p.kill(ctx, sig, all)
}

func (s *createdCheckpointState) SetExited(status int) {
	s.p.setExited(status)

	if err := s.transition("stopped"); err != nil {
		panic(err)
	}
}

func (s *createdCheckpointState) Exec(ctx context.Context, path string, r *ExecConfig) (Process, error) {
	return nil, errors.Errorf("cannot exec in a created state")
}

func (s *createdCheckpointState) Status(ctx context.Context) (string, error) {
	return "created", nil
}

type runningState struct {
	p *Init
}

func (s *runningState) transition(name string) error {
	switch name {
	case "stopped":
		s.p.initState = &stoppedState{p: s.p}
	case "paused":
		s.p.initState = &pausedState{p: s.p}
	default:
		return errors.Errorf("invalid state transition %q to %q", stateName(s), name)
	}
	return nil
}

func (s *runningState) Pause(ctx context.Context) error {
	s.p.pausing.set(true)
	// NOTE "pausing" will be returned in the short window
	// after `transition("paused")`, before `pausing` is reset
	// to false. That doesn't break the state machine, just
	// delays the "paused" state a little bit.
	defer s.p.pausing.set(false)

	if err := s.p.runtime.Pause(ctx, s.p.id); err != nil {
		return s.p.runtimeError(err, "OCI runtime pause failed")
	}

	return s.transition("paused")
}

func (s *runningState) Resume(ctx context.Context) error {
	return errors.Errorf("cannot resume a running process")
}

func (s *runningState) Update(ctx context.Context, r *google_protobuf.Any) error {
	return s.p.update(ctx, r)
}

func (s *runningState) Checkpoint(ctx context.Context, r *CheckpointConfig) error {
	return s.p.checkpoint(ctx, r)
}

func (s *runningState) Start(ctx context.Context) error {
	return errors.Errorf("cannot start a running process")
}

func (s *runningState) Delete(ctx context.Context) error {
	return errors.Errorf("cannot delete a running process")
}

func (s *runningState) Kill(ctx context.Context, sig uint32, all bool) error {
	return s.p.kill(ctx, sig, all)
}

func (s *runningState) SetExited(status int) {
	s.p.setExited(status)

	if err := s.transition("stopped"); err != nil {
		panic(err)
	}
}

func (s *runningState) Exec(ctx context.Context, path string, r *ExecConfig) (Process, error) {
	return s.p.exec(ctx, path, r)
}

func (s *runningState) Status(ctx context.Context) (string, error) {
	return "running", nil
}

type pausedState struct {
	p *Init
}

func (s *pausedState) transition(name string) error {
	switch name {
	case "running":
		s.p.initState = &runningState{p: s.p}
	case "stopped":
		s.p.initState = &stoppedState{p: s.p}
	default:
		return errors.Errorf("invalid state transition %q to %q", stateName(s), name)
	}
	return nil
}

func (s *pausedState) Pause(ctx context.Context) error {
	return errors.Errorf("cannot pause a paused container")
}

func (s *pausedState) Resume(ctx context.Context) error {
	if err := s.p.runtime.Resume(ctx, s.p.id); err != nil {
		return s.p.runtimeError(err, "OCI runtime resume failed")
	}

	return s.transition("running")
}

func (s *pausedState) Update(ctx context.Context, r *google_protobuf.Any) error {
	return s.p.update(ctx, r)
}

func (s *pausedState) Checkpoint(ctx context.Context, r *CheckpointConfig) error {
	return s.p.checkpoint(ctx, r)
}

func (s *pausedState) Start(ctx context.Context) error {
	return errors.Errorf("cannot start a paused process")
}

func (s *pausedState) Delete(ctx context.Context) error {
	return errors.Errorf("cannot delete a paused process")
}

func (s *pausedState) Kill(ctx context.Context, sig uint32, all bool) error {
	return s.p.kill(ctx, sig, all)
}

func (s *pausedState) SetExited(status int) {
	s.p.setExited(status)

	if err := s.p.runtime.Resume(context.Background(), s.p.id); err != nil {
		logrus.WithError(err).Error("resuming exited container from paused state")
	}

	if err := s.transition("stopped"); err != nil {
		panic(err)
	}
}

func (s *pausedState) Exec(ctx context.Context, path string, r *ExecConfig) (Process, error) {
	return nil, errors.Errorf("cannot exec in a paused state")
}

func (s *pausedState) Status(ctx context.Context) (string, error) {
	return "paused", nil
}

type stoppedState struct {
	p *Init
}

func (s *stoppedState) transition(name string) error {
	switch name {
	case "deleted":
		s.p.initState = &deletedState{}
	default:
		return errors.Errorf("invalid state transition %q to %q", stateName(s), name)
	}
	return nil
}

func (s *stoppedState) Pause(ctx context.Context) error {
	return errors.Errorf("cannot pause a stopped container")
}

func (s *stoppedState) Resume(ctx context.Context) error {
	return errors.Errorf("cannot resume a stopped container")
}

func (s *stoppedState) Update(ctx context.Context, r *google_protobuf.Any) error {
	return errors.Errorf("cannot update a stopped container")
}

func (s *stoppedState) Checkpoint(ctx context.Context, r *CheckpointConfig) error {
	return errors.Errorf("cannot checkpoint a stopped container")
}

func (s *stoppedState) Start(ctx context.Context) error {
	return errors.Errorf("cannot start a stopped process")
}

func (s *stoppedState) Delete(ctx context.Context) error {
	if err := s.p.delete(ctx); err != nil {
		return err
	}
	return s.transition("deleted")
}

func (s *stoppedState) Kill(ctx context.Context, sig uint32, all bool) error {
	return s.p.kill(ctx, sig, all)
}

func (s *stoppedState) SetExited(status int) {
	// no op
}

func (s *stoppedState) Exec(ctx context.Context, path string, r *ExecConfig) (Process, error) {
	return nil, errors.Errorf("cannot exec in a stopped state")
}

func (s *stoppedState) Status(ctx context.Context) (string, error) {
	return "stopped", nil
}
