// We'd love a working pure Go implementation. But so far we didn't find any
// that would work for us. src-d/go-git is innapropriate due to
// https://github.com/src-d/go-git/issues/793 and
// https://github.com/src-d/go-git/issues/785 . And binding to the libgit C lib
// aren't pure Go either. So we need the git binary for now.

// Package git makes a git repository out of a local directory, keeps the
// content committed when the directory changes, and optionaly (if a remote
// repos url is provided), keep it in sync with a remote repository.
package git

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/spf13/afero"

	"github.com/bpineau/katafygio/config"
	"github.com/sirupsen/logrus"
)

var (
	timeoutCommands = 60 * time.Second
	checkInterval   = 10 * time.Second
)

var appFs = afero.NewOsFs()

// Store will maintain a git repository off dumped kube objects
type Store struct {
	Logger   *logrus.Logger
	URL      string
	LocalDir string
	Author   string
	Email    string
	Msg      string
	DryRun   bool
	stopch   chan struct{}
	donech   chan struct{}
}

// New instantiate a new git Store
func New(config *config.KfConfig) *Store {
	return &Store{
		Logger:   config.Logger,
		URL:      config.GitURL,
		LocalDir: config.LocalDir,
		Author:   "Katafygio", // XXX should we expose a cli option for that?
		Email:    "katafygio@localhost",
		Msg:      "Kubernetes cluster change",
		DryRun:   config.DryRun,
	}
}

// Start maintains a directory content committed
func (s *Store) Start() (*Store, error) {
	s.Logger.Info("Starting git repository synchronizer")
	s.stopch = make(chan struct{})
	s.donech = make(chan struct{})

	err := s.Clone()
	if err != nil {
		return nil, err
	}

	go func() {
		checkTick := time.NewTicker(checkInterval)
		defer checkTick.Stop()
		defer close(s.donech)

		for {
			select {
			case <-checkTick.C:
				s.commitAndPush()
			case <-s.stopch:
				return
			}
		}
	}()

	return s, nil
}

// Stop stops the git goroutine
func (s *Store) Stop() {
	s.Logger.Info("Stopping git repository synchronizer")
	close(s.stopch)
	<-s.donech
}

// Git wraps the git command
func (s *Store) Git(args ...string) error {
	if s.DryRun {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutCommands)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...) // #nosec
	cmd.Dir = s.LocalDir

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s failed with code %v: %s", args[0], err, out)
	}

	return nil
}

// Status tests the git status of a repository
func (s *Store) Status() (changed bool, err error) {
	if s.DryRun {
		return false, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutCommands)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain") // #nosec
	cmd.Dir = s.LocalDir

	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git status failed with code %v: %s", err, out)
	}

	if len(out) != 0 {
		return true, nil
	}

	return false, nil
}

// Clone does git clone, or git init (when there's no GitURL to clone from)
func (s *Store) Clone() (err error) {
	if !s.DryRun {
		err = appFs.MkdirAll(s.LocalDir, 0700)
		if err != nil {
			return fmt.Errorf("failed to create %s: %v", s.LocalDir, err)
		}
	}

	if s.URL == "" {
		err = s.Git("init", s.LocalDir)
	} else {
		err = s.Git("clone", s.URL, s.LocalDir)
	}

	if err != nil {
		return fmt.Errorf("failed to init or clone in %s: %v", s.LocalDir, err)
	}

	err = s.Git("config", "user.name", s.Author)
	if err != nil {
		return fmt.Errorf("failed to config git user.name %s in %s: %v",
			s.Author, s.LocalDir, err)
	}

	err = s.Git("config", "user.email", s.Email)
	if err != nil {
		return fmt.Errorf("failed to config git user.email %s in %s: %v",
			s.Email, s.LocalDir, err)
	}

	return nil
}

// Commit git commit all the directory's changes
func (s *Store) Commit() (changed bool, err error) {
	changed, err = s.Status()
	if err != nil {
		return changed, err
	}

	if !changed {
		return false, nil
	}

	err = s.Git("add", "-A")
	if err != nil {
		return false, fmt.Errorf("failed to git add -A: %v", err)
	}

	err = s.Git("commit", "-m", s.Msg)
	if err != nil {
		return false, fmt.Errorf("failed to git commit: %v", err)
	}

	return true, nil
}

// Push git push to the origin
func (s *Store) Push() error {
	err := s.Git("push")
	if err != nil {
		return fmt.Errorf("failed to git push: %v", err)
	}

	return nil
}

func (s *Store) commitAndPush() {
	changed, err := s.Commit()
	if err != nil {
		s.Logger.Warn(err)
	}

	if !changed || s.URL == "" {
		return
	}

	err = s.Push()
	if err != nil {
		s.Logger.Warn(err)
	}
}
