package local_manager

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/langgenius/dify-plugin-daemon/internal/process"
	"github.com/langgenius/dify-plugin-daemon/internal/types/entities/plugin_entities"
	"github.com/langgenius/dify-plugin-daemon/internal/utils/log"
	"github.com/langgenius/dify-plugin-daemon/internal/utils/routine"
)

func (r *LocalPluginRuntime) gc() {
	if r.io_identity != "" {
		RemoveStdio(r.io_identity)
	}

	if r.wait_chan != nil {
		close(r.wait_chan)
		r.wait_chan = nil
	}
}

func (r *LocalPluginRuntime) init() {
	r.wait_chan = make(chan bool)
	r.SetLaunching()
}

func (r *LocalPluginRuntime) Type() plugin_entities.PluginRuntimeType {
	return plugin_entities.PLUGIN_RUNTIME_TYPE_LOCAL
}

func (r *LocalPluginRuntime) StartPlugin() error {
	defer log.Info("plugin %s stopped", r.Config.Identity())

	r.init()
	// start plugin
	// TODO: use exec.Command("bash") instead of exec.Command("bash", r.Config.Execution.Launch)
	e := exec.Command("bash")
	e.Dir = r.State.AbsolutePath
	// add env INSTALL_METHOD=local
	e.Env = append(e.Env, "INSTALL_METHOD=local", "PATH="+os.Getenv("PATH"))

	// NOTE: subprocess will be taken care of by subprocess manager
	// ensure all subprocess are killed when parent process exits, especially on Golang debugger
	process.WrapProcess(e)

	// get writer
	stdin, err := e.StdinPipe()
	if err != nil {
		r.SetRestarting()
		return fmt.Errorf("get stdin pipe failed: %s", err.Error())
	}
	defer stdin.Close()

	stdout, err := e.StdoutPipe()
	if err != nil {
		r.SetRestarting()
		return fmt.Errorf("get stdout pipe failed: %s", err.Error())
	}
	defer stdout.Close()

	stderr, err := e.StderrPipe()
	if err != nil {
		r.SetRestarting()
		return fmt.Errorf("get stderr pipe failed: %s", err.Error())
	}
	defer stderr.Close()

	if err := e.Start(); err != nil {
		r.SetRestarting()
		return err
	}

	// add to subprocess manager
	process.NewProcess(e)
	defer process.RemoveProcess(e)

	defer func() {
		// wait for plugin to exit
		err = e.Wait()
		if err != nil {
			r.SetRestarting()
			log.Error("plugin %s exited with error: %s", r.Config.Identity(), err.Error())
		}

		r.gc()
	}()
	defer e.Process.Kill()

	log.Info("plugin %s started", r.Config.Identity())

	// setup stdio
	stdio := PutStdioIo(r.Config.Identity(), stdin, stdout, stderr)
	r.io_identity = stdio.GetID()
	defer stdio.Stop()

	wg := sync.WaitGroup{}
	wg.Add(2)

	// listen to plugin stdout
	routine.Submit(func() {
		defer wg.Done()
		stdio.StartStdout()
	})

	// listen to plugin stderr
	routine.Submit(func() {
		defer wg.Done()
		stdio.StartStderr()
	})

	// wait for plugin to exit
	err = stdio.Wait()
	if err != nil {
		return err
	}

	wg.Wait()

	// plugin has exited
	r.SetPending()
	return nil
}

func (r *LocalPluginRuntime) Wait() (<-chan bool, error) {
	if r.wait_chan == nil {
		return nil, errors.New("plugin not started")
	}
	return r.wait_chan, nil
}
