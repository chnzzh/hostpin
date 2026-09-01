package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"

	"golang.org/x/crypto/argon2"
)

const (
	containerMagic   = "HOSTPIN-BACKUP\x00"
	containerVersion = 1
	containerChunk   = 1 << 20
	maxHeaderBytes   = 64 << 10
)

var (
	ErrInvalidBackup     = errors.New("invalid Hostpin backup")
	ErrInvalidPassphrase = errors.New("backup passphrase is incorrect or the backup is damaged")
	ErrBackupBusy        = errors.New("another backup or restore operation is already running")
	ErrBackupUnsupported = errors.New("one-click backup and restore is available only for SQLite")
	ErrMasterKeyMismatch = errors.New("backup master key does not match the configured external master key")
)

type containerHeader struct {
	Format      string `json:"format"`
	Version     int    `json:"version"`
	KDF         string `json:"kdf"`
	MemoryKiB   uint32 `json:"memory_kib"`
	Iterations  uint32 `json:"iterations"`
	Parallelism uint8  `json:"parallelism"`
	Salt        string `json:"salt"`
	NoncePrefix string `json:"nonce_prefix"`
	ChunkSize   uint32 `json:"chunk_size"`
}

func validatePassphrase(passphrase string) error {
	if len(passphrase) < 12 || len(passphrase) > 1024 {
		return errors.New("backup passphrase must contain 12 to 1024 characters")
	}
	return nil
}

func encryptContainer(destination io.Writer, source io.Reader, passphrase string) error {
	if err := validatePassphrase(passphrase); err != nil {
		return err
	}
	salt := make([]byte, 16)
	noncePrefix := make([]byte, 4)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return fmt.Errorf("generate backup salt: %w", err)
	}
	if _, err := io.ReadFull(rand.Reader, noncePrefix); err != nil {
		return fmt.Errorf("generate backup nonce: %w", err)
	}
	header := containerHeader{
		Format: "hostpin-encrypted-backup", Version: containerVersion, KDF: "argon2id",
		MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: uint8(min(runtime.NumCPU(), 2)),
		Salt:        base64.RawStdEncoding.EncodeToString(salt),
		NoncePrefix: base64.RawStdEncoding.EncodeToString(noncePrefix), ChunkSize: containerChunk,
	}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(destination, containerMagic); err != nil {
		return err
	}
	if err := writeUint32(destination, uint32(len(headerBytes))); err != nil {
		return err
	}
	if _, err := destination.Write(headerBytes); err != nil {
		return err
	}
	aead, err := backupAEAD(passphrase, salt, header.MemoryKiB, header.Iterations, header.Parallelism)
	if err != nil {
		return err
	}
	headerHash := sha256.Sum256(headerBytes)
	buffer := make([]byte, int(header.ChunkSize))
	var counter uint64
	for {
		count, readErr := io.ReadFull(source, buffer)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return fmt.Errorf("read backup payload: %w", readErr)
		}
		if count == 0 {
			break
		}
		nonce := backupNonce(noncePrefix, counter)
		aad := backupAAD(headerHash, counter, uint32(count))
		ciphertext := aead.Seal(nil, nonce, buffer[:count], aad)
		if err := writeUint32(destination, uint32(count)); err != nil {
			return err
		}
		if _, err := destination.Write(ciphertext); err != nil {
			return err
		}
		counter++
		if errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
	}
	return writeUint32(destination, 0)
}

func decryptContainer(destination io.Writer, source io.Reader, passphrase string, maximum int64) error {
	if err := validatePassphrase(passphrase); err != nil {
		return err
	}
	magic := make([]byte, len(containerMagic))
	if _, err := io.ReadFull(source, magic); err != nil || string(magic) != containerMagic {
		return ErrInvalidBackup
	}
	headerLength, err := readUint32(source)
	if err != nil || headerLength == 0 || headerLength > maxHeaderBytes {
		return ErrInvalidBackup
	}
	headerBytes := make([]byte, headerLength)
	if _, err := io.ReadFull(source, headerBytes); err != nil {
		return ErrInvalidBackup
	}
	var header containerHeader
	if json.Unmarshal(headerBytes, &header) != nil || header.Format != "hostpin-encrypted-backup" ||
		header.Version != containerVersion || header.KDF != "argon2id" || header.MemoryKiB < 8*1024 ||
		header.MemoryKiB > 128*1024 || header.Iterations == 0 || header.Iterations > 6 ||
		header.Parallelism == 0 || header.Parallelism > 4 || header.ChunkSize < 64<<10 || header.ChunkSize > 4<<20 {
		return ErrInvalidBackup
	}
	salt, err := base64.RawStdEncoding.DecodeString(header.Salt)
	if err != nil || len(salt) != 16 {
		return ErrInvalidBackup
	}
	noncePrefix, err := base64.RawStdEncoding.DecodeString(header.NoncePrefix)
	if err != nil || len(noncePrefix) != 4 {
		return ErrInvalidBackup
	}
	aead, err := backupAEAD(passphrase, salt, header.MemoryKiB, header.Iterations, header.Parallelism)
	if err != nil {
		return err
	}
	headerHash := sha256.Sum256(headerBytes)
	var total int64
	for counter := uint64(0); ; counter++ {
		plainLength, err := readUint32(source)
		if err != nil {
			return ErrInvalidBackup
		}
		if plainLength == 0 {
			var trailing [1]byte
			if _, trailingErr := io.ReadFull(source, trailing[:]); !errors.Is(trailingErr, io.EOF) {
				return ErrInvalidBackup
			}
			return nil
		}
		if plainLength > header.ChunkSize || total+int64(plainLength) > maximum {
			return fmt.Errorf("%w: decrypted payload exceeds the size limit", ErrInvalidBackup)
		}
		ciphertext := make([]byte, int(plainLength)+aead.Overhead())
		if _, err := io.ReadFull(source, ciphertext); err != nil {
			return ErrInvalidBackup
		}
		plaintext, err := aead.Open(nil, backupNonce(noncePrefix, counter), ciphertext, backupAAD(headerHash, counter, plainLength))
		if err != nil {
			return ErrInvalidPassphrase
		}
		if _, err := destination.Write(plaintext); err != nil {
			return err
		}
		total += int64(len(plaintext))
	}
}

func backupAEAD(passphrase string, salt []byte, memory, iterations uint32, parallelism uint8) (cipher.AEAD, error) {
	key := argon2.IDKey([]byte(passphrase), salt, iterations, memory, parallelism, 32)
	block, err := aes.NewCipher(key)
	clear(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func backupNonce(prefix []byte, counter uint64) []byte {
	nonce := make([]byte, 12)
	copy(nonce, prefix)
	binary.BigEndian.PutUint64(nonce[4:], counter)
	return nonce
}

func backupAAD(headerHash [32]byte, counter uint64, length uint32) []byte {
	aad := make([]byte, 44)
	copy(aad, headerHash[:])
	binary.BigEndian.PutUint64(aad[32:], counter)
	binary.BigEndian.PutUint32(aad[40:], length)
	return aad
}

func writeUint32(writer io.Writer, value uint32) error {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	_, err := writer.Write(encoded[:])
	return err
}

func readUint32(reader io.Reader) (uint32, error) {
	var encoded [4]byte
	if _, err := io.ReadFull(reader, encoded[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(encoded[:]), nil
}
