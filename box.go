package tarantool

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
)

// Box is tarantool instance. For start/stop tarantool in tests
type Box struct {
	Root     string
	Port     uint
	cmd      *exec.Cmd
	stderr   io.ReadCloser
	stopOnce sync.Once
	stopped  chan bool
	initLua  string
}

type BoxOptions struct {
	Listen  uint
	PortMin uint
	PortMax uint
}

var (
	ErrPortAlreadyInUse = errors.New("Port already in use")
)

func NewBox(config string, options *BoxOptions) (*Box, error) {
	if options == nil {
		options = &BoxOptions{}
	}

	if options.PortMin == 0 {
		options.PortMin = 8000
	}

	if options.PortMax == 0 {
		options.PortMax = 9000
	}

	if options.Listen != 0 {
		options.PortMin = options.Listen
		options.PortMax = options.Listen
	}

	var box *Box

	for port := options.PortMin; port <= options.PortMax; port++ {

		tmpDir, err := ioutil.TempDir("", "") //os.RemoveAll(tmpDir);
		if err != nil {
			return nil, err
		}

		initLua := `
			box.cfg{
				listen = {port},
				snap_dir = "{root}/snap/",
				sophia_dir = "{root}/sophia/",
				wal_dir = "{root}/wal/"
			}
			box.once('guest:read_universe', function()
				box.schema.user.grant('guest', 'read', 'universe', {if_not_exists = true})
			end)
		`

		initLua = strings.Replace(initLua, "{port}", fmt.Sprintf("%d", port), -1)
		initLua = strings.Replace(initLua, "{root}", tmpDir, -1)
		initLua = fmt.Sprintf("%s\n%s", initLua, config)

		initLuaFile := path.Join(tmpDir, "init.lua")
		err = ioutil.WriteFile(initLuaFile, []byte(initLua), 0644)
		if err != nil {
			return nil, err
		}

		for _, subDir := range []string{"sophia", "snap", "wal"} {
			err = os.Mkdir(path.Join(tmpDir, subDir), 0755)
			if err != nil {
				return nil, err
			}
		}

		box = &Box{
			Root:    tmpDir,
			Port:    port,
			cmd:     nil,
			stopped: make(chan bool),
			stderr:  nil,
			initLua: initLuaFile,
		}
		close(box.stopped)

		err = box.Start()
		if err == nil {
			break
		}
		if err != ErrPortAlreadyInUse {
			return nil, err
		}
		os.RemoveAll(box.Root)
		box = nil
	}

	if box == nil {
		return nil, fmt.Errorf("Can't bind any port from %d to %d", options.PortMin, options.PortMax)
	}

	return box, nil
}

func (box *Box) Start() error {
	if !box.IsStopped() {
		return nil
	}

	box.stopped = make(chan bool)

	cmd := exec.Command("tarantool", box.initLua)
	boxStderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	err = cmd.Start()
	if err != nil {
		return err
	}


	var boxStderrBuffer bytes.Buffer

	p := make([]byte, 1024)

	box.cmd = cmd
	box.stderr = boxStderr

	for {
		if strings.Contains(boxStderrBuffer.String(), "entering the event loop") {
			break
		}

		if strings.Contains(boxStderrBuffer.String(), "is already in use, will retry binding after") {
			box.Close()
			return ErrPortAlreadyInUse
		}

		n, err := boxStderr.Read(p)
		if n > 0 {
			boxStderrBuffer.Write(p[:n])
		}
		if err != nil {
			fmt.Println(boxStderrBuffer.String())
			box.Close()
			return err
		}
	}

	// print logs
	go func() {
		p := make([]byte, 1024)

		for {
			n, err := box.stderr.Read(p)
			if err != nil {
				return
			}
			fmt.Println(string(p[:n]))
		}
	}()

	return nil
}

func (box *Box) Stop() {
	go func() {
		select {
			case <-box.stopped:
				return
			default:
				if box.cmd != nil {
					box.cmd.Process.Kill()
					box.cmd.Process.Wait()
					box.cmd = nil
				}
				close(box.stopped)
		}
	}()
	<-box.stopped
}

func (box *Box) IsStopped() bool {
	select {
		case <-box.stopped:
			return true
		default:
			return false
	}
}

func (box *Box) Close() {
	box.stopOnce.Do(func() {
		box.Stop()
		os.RemoveAll(box.Root)
	})
}

func (box *Box) Addr() string {
	return fmt.Sprintf("127.0.0.1:%d", box.Port)
}

func (box *Box) Connect(options *Options) (*Connection, error) {
	return Connect(box.Addr(), options)
}
