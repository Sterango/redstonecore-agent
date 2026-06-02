package gameserver

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// A2SInfo is the subset of the Steam A2S_INFO response we use.
type A2SInfo struct {
	Name       string
	Map        string
	Players    int
	MaxPlayers int
}

// QueryA2S sends a Steam A2S_INFO query to a UDP host:port and returns the
// parsed info, handling the challenge-response. Short timeout; meant for polling.
func QueryA2S(addr string) (*A2SInfo, error) {
	conn, err := net.DialTimeout("udp", addr, 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	req := append([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x54}, []byte("Source Engine Query\x00")...)
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}

	buf := make([]byte, 1400)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	resp := buf[:n]

	// Challenge response (0x41): resend the query with the 4-byte challenge.
	if n >= 9 && resp[4] == 0x41 {
		req = append(req, resp[5:9]...)
		if _, err := conn.Write(req); err != nil {
			return nil, err
		}
		n, err = conn.Read(buf)
		if err != nil {
			return nil, err
		}
		resp = buf[:n]
	}

	if n < 6 || resp[4] != 0x49 {
		return nil, fmt.Errorf("unexpected A2S response")
	}

	r := bytes.NewReader(resp[5:])
	if _, err := r.ReadByte(); err != nil { // protocol
		return nil, err
	}
	name, _ := readCString(r)
	mapName, _ := readCString(r)
	_, _ = readCString(r) // folder
	_, _ = readCString(r) // game
	var appid uint16
	_ = binary.Read(r, binary.LittleEndian, &appid)
	players, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	maxPlayers, _ := r.ReadByte()

	return &A2SInfo{Name: name, Map: mapName, Players: int(players), MaxPlayers: int(maxPlayers)}, nil
}

func readCString(r *bytes.Reader) (string, error) {
	var b []byte
	for {
		c, err := r.ReadByte()
		if err != nil {
			return string(b), err
		}
		if c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b), nil
}
