package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/pkg/term"
)

func main() {
	log.SetPrefix("lk: ")
	log.SetFlags(0)

	dev := os.ExpandEnv("$HOME/Library/Containers/com.docker.docker/Data/vms/0/tty")
	t, err := term.Open(dev, term.RawMode, term.ReadTimeout(2*time.Second))
	if err != nil {
		log.Fatal(err)
	}
	defer t.Close()
	defer t.Restore()

	fmt.Fprintln(t)
	fmt.Fprintln(t, "ls /")
	s := bufio.NewScanner(t)
	for i := 0; i < 10; i++ {
		time.Sleep(10 * time.Millisecond)
		if !s.Scan() {
			break
		}
		fmt.Println(s.Text())
	}
}
