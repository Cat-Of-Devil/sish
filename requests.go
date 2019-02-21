package main

import (
	"io"
	"io/ioutil"
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

type channelForwardMsg struct {
	Addr  string
	Rport uint32
}

type forwardedTCPPayload struct {
	Addr       string
	Port       uint32
	OriginAddr string
	OriginPort uint32
}

func handleRemoteForward(newRequest *ssh.Request, sshConn *SSHConnection, state *State) {
	check := &channelForwardMsg{}

	ssh.Unmarshal(newRequest.Payload, check)

	stringPort := strconv.FormatUint(uint64(check.Rport), 10)
	listenAddr := check.Addr + ":" + stringPort
	listenType := "tcp"

	tmpfile, err := ioutil.TempFile("", sshConn.SSHConn.RemoteAddr().String()+":"+stringPort)
	if err != nil {
		newRequest.Reply(false, nil)
		return
	}
	os.Remove(tmpfile.Name())

	if stringPort == "80" || stringPort == "443" {
		listenType = "unix"
		listenAddr = tmpfile.Name()
	}

	chanListener, err := net.Listen(listenType, listenAddr)
	if err != nil {
		newRequest.Reply(false, nil)
		return
	}

	state.Listeners.Store(chanListener.Addr(), chanListener)
	sshConn.Listeners.Store(chanListener.Addr(), chanListener)

	defer func() {
		chanListener.Close()
		state.Listeners.Delete(chanListener.Addr())
		sshConn.Listeners.Delete(chanListener.Addr())
		os.Remove(tmpfile.Name())
	}()

	if stringPort == "80" || stringPort == "443" {
		scheme := "http"
		if stringPort == "443" {
			scheme = "https"
		}

		host := strings.ToLower(RandStringBytesMaskImprSrc(*domainLen) + "." + *rootDomain)

		pH := &ProxyHolder{
			ProxyHost: host,
			ProxyTo:   chanListener.Addr().String(),
			Scheme:    scheme,
		}

		state.HTTPListeners.Store(host, pH)
		defer state.HTTPListeners.Delete(host)

		sshConn.Messages <- "HTTP requests for 80 and 443 can be reached on host: " + host
	} else {
		sshConn.Messages <- "Connections being forwarded to " + chanListener.Addr().String()
	}

	go func() {
		for {
			select {
			case <-sshConn.Close:
				chanListener.Close()
				return
			default:
				break
			}
		}
	}()

	for {
		cl, err := chanListener.Accept()
		if err != nil {
			break
		}

		defer cl.Close()

		resp := &forwardedTCPPayload{
			Addr:       check.Addr,
			Port:       check.Rport,
			OriginAddr: check.Addr,
			OriginPort: check.Rport,
		}

		newChan, newReqs, err := sshConn.SSHConn.OpenChannel("forwarded-tcpip", ssh.Marshal(resp))
		if err != nil {
			sshConn.Messages <- err.Error()
			cl.Close()
			continue
		}

		defer newChan.Close()

		go copyBoth(cl, newChan)
		go ssh.DiscardRequests(newReqs)
	}
}

func copyBoth(writer net.Conn, reader ssh.Channel) {
	defer func() {
		writer.Close()
		reader.Close()
	}()

	go io.Copy(writer, reader)
	io.Copy(reader, writer)
}