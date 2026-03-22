package maprender

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// NBT tag types
const (
	tagEnd       = 0
	tagByte      = 1
	tagShort     = 2
	tagInt       = 3
	tagLong      = 4
	tagFloat     = 5
	tagDouble    = 6
	tagByteArray = 7
	tagString    = 8
	tagList      = 9
	tagCompound  = 10
	tagIntArray  = 11
	tagLongArray = 12
)

// NBTReader reads NBT data from a byte stream
type NBTReader struct {
	r   io.Reader
	buf [8]byte
}

// NewNBTReader creates an NBT reader, auto-detecting compression
func NewNBTReader(data []byte) (*NBTReader, error) {
	var reader io.Reader

	if len(data) < 2 {
		return nil, fmt.Errorf("data too short")
	}

	// Check for gzip magic bytes
	if data[0] == 0x1f && data[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		reader = gz
	} else if data[0] == 0x78 { // zlib magic
		zr, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("zlib: %w", err)
		}
		reader = zr
	} else {
		reader = bytes.NewReader(data)
	}

	return &NBTReader{r: reader}, nil
}

// ReadTag reads a named tag and returns its type, name, and value
func (n *NBTReader) ReadTag() (byte, string, interface{}, error) {
	tagType, err := n.readByte()
	if err != nil {
		return 0, "", nil, err
	}
	if tagType == tagEnd {
		return tagEnd, "", nil, nil
	}

	name, err := n.readString()
	if err != nil {
		return 0, "", nil, err
	}

	val, err := n.readPayload(tagType)
	if err != nil {
		return 0, "", nil, err
	}

	return tagType, name, val, nil
}

// ReadCompound reads a full compound tag (assumes the compound tag header is already read)
func (n *NBTReader) ReadCompound() (map[string]interface{}, error) {
	result := make(map[string]interface{})
	for {
		tagType, name, val, err := n.ReadTag()
		if err != nil {
			return nil, err
		}
		if tagType == tagEnd {
			break
		}
		result[name] = val
	}
	return result, nil
}

func (n *NBTReader) readPayload(tagType byte) (interface{}, error) {
	switch tagType {
	case tagByte:
		return n.readByte()
	case tagShort:
		return n.readShort()
	case tagInt:
		return n.readInt()
	case tagLong:
		return n.readLong()
	case tagFloat:
		return n.readFloat()
	case tagDouble:
		return n.readDouble()
	case tagByteArray:
		return n.readByteArray()
	case tagString:
		return n.readString()
	case tagList:
		return n.readList()
	case tagCompound:
		return n.ReadCompound()
	case tagIntArray:
		return n.readIntArray()
	case tagLongArray:
		return n.readLongArray()
	default:
		return nil, fmt.Errorf("unknown tag type: %d", tagType)
	}
}

func (n *NBTReader) readByte() (byte, error) {
	_, err := io.ReadFull(n.r, n.buf[:1])
	return n.buf[0], err
}

func (n *NBTReader) readShort() (int16, error) {
	_, err := io.ReadFull(n.r, n.buf[:2])
	return int16(binary.BigEndian.Uint16(n.buf[:2])), err
}

func (n *NBTReader) readInt() (int32, error) {
	_, err := io.ReadFull(n.r, n.buf[:4])
	return int32(binary.BigEndian.Uint32(n.buf[:4])), err
}

func (n *NBTReader) readLong() (int64, error) {
	_, err := io.ReadFull(n.r, n.buf[:8])
	return int64(binary.BigEndian.Uint64(n.buf[:8])), err
}

func (n *NBTReader) readFloat() (float32, error) {
	_, err := io.ReadFull(n.r, n.buf[:4])
	bits := binary.BigEndian.Uint32(n.buf[:4])
	return float32FromBits(bits), err
}

func (n *NBTReader) readDouble() (float64, error) {
	_, err := io.ReadFull(n.r, n.buf[:8])
	bits := binary.BigEndian.Uint64(n.buf[:8])
	return float64FromBits(bits), err
}

func (n *NBTReader) readString() (string, error) {
	length, err := n.readShort()
	if err != nil {
		return "", err
	}
	if length <= 0 {
		return "", nil
	}
	buf := make([]byte, length)
	_, err = io.ReadFull(n.r, buf)
	return string(buf), err
}

func (n *NBTReader) readByteArray() ([]byte, error) {
	length, err := n.readInt()
	if err != nil {
		return nil, err
	}
	if length <= 0 {
		return nil, nil
	}
	buf := make([]byte, length)
	_, err = io.ReadFull(n.r, buf)
	return buf, err
}

func (n *NBTReader) readIntArray() ([]int32, error) {
	length, err := n.readInt()
	if err != nil {
		return nil, err
	}
	if length <= 0 {
		return nil, nil
	}
	arr := make([]int32, length)
	for i := int32(0); i < length; i++ {
		arr[i], err = n.readInt()
		if err != nil {
			return nil, err
		}
	}
	return arr, nil
}

func (n *NBTReader) readLongArray() ([]int64, error) {
	length, err := n.readInt()
	if err != nil {
		return nil, err
	}
	if length <= 0 {
		return nil, nil
	}
	arr := make([]int64, length)
	for i := int32(0); i < length; i++ {
		arr[i], err = n.readLong()
		if err != nil {
			return nil, err
		}
	}
	return arr, nil
}

func (n *NBTReader) readList() (interface{}, error) {
	elemType, err := n.readByte()
	if err != nil {
		return nil, err
	}
	length, err := n.readInt()
	if err != nil {
		return nil, err
	}
	if length <= 0 {
		return []interface{}{}, nil
	}

	list := make([]interface{}, length)
	for i := int32(0); i < length; i++ {
		list[i], err = n.readPayload(elemType)
		if err != nil {
			return nil, err
		}
	}
	return list, nil
}

// Helper functions for float conversion
func float32FromBits(bits uint32) float32 {
	return math.Float32frombits(bits)
}

func float64FromBits(bits uint64) float64 {
	return math.Float64frombits(bits)
}
