package bot

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	privateKeyPath = "private_key.pem"
	publicKeyPath  = "public_key.pub"
	bits           = 2048
)

func GenerateAndSaveSSHKeyPairIfNotExist() error {
	if KeyFilesExist(privateKeyPath, publicKeyPath) {
		return nil
	}

	privateKey, publicKey, err := GenerateSSHKeyPair(bits)
	if err != nil {
		return err
	}

	err = SavePrivateKeyToFile(privateKeyPath, privateKey)
	if err != nil {
		return err
	}

	err = SavePublicKeyToFile(publicKeyPath, publicKey)
	if err != nil {
		return err
	}

	return nil
}

func GenerateAndSaveSSHKeyPair() error {
	privateKey, publicKey, err := GenerateSSHKeyPair(bits)
	if err != nil {
		return err
	}

	err = SavePrivateKeyToFile(privateKeyPath, privateKey)
	if err != nil {
		return err
	}

	err = SavePublicKeyToFile(publicKeyPath, publicKey)
	if err != nil {
		return err
	}

	return nil
}

func KeyFilesExist(privateKeyPath, publicKeyPath string) bool {
	_, privateKeyErr := os.Stat(privateKeyPath)
	_, publicKeyErr := os.Stat(publicKeyPath)
	return !os.IsNotExist(privateKeyErr) && !os.IsNotExist(publicKeyErr)
}

func GenerateSSHKeyPair(bits int) (*rsa.PrivateKey, ssh.PublicKey, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return nil, nil, err
	}

	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, nil, err
	}

	return privateKey, publicKey, nil
}

func SavePrivateKeyToFile(filename string, privateKey *rsa.PrivateKey) error {
	privateKeyBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	privateKeyPEM := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateKeyBytes,
	}

	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	err = pem.Encode(file, privateKeyPEM)
	if err != nil {
		return err
	}

	return nil
}

func SavePublicKeyToFile(filename string, publicKey ssh.PublicKey) error {
	publicKeyBytes := ssh.MarshalAuthorizedKey(publicKey)

	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(string(publicKeyBytes))
	if err != nil {
		return err
	}

	return nil
}

func SSHConnectToRemoteServer(connectionDetails string) error {
	privateKey, err := LoadPrivateKey(privateKeyPath)
	if err != nil {
		return err
	}

	config := &ssh.ClientConfig{
		User: "username", // You will replace this with the actual username extracted from connectionDetails
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(privateKey),
		},
		// Other configuration options like HostKeyCallback, etc.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	// Extract username, host, and port from connectionDetails
	parts := strings.Split(connectionDetails, "@")
	if len(parts) != 2 {
		return errors.New("invalid connection format")
	}
	username := parts[0]
	hostPort := parts[1]

	config.User = username // Set the actual username

	// Connect to the remote server
	client, err := ssh.Dial("tcp", hostPort, config)
	if err != nil {
		return err
	}
	defer client.Close()

	// Now you have a connected SSH client to the remote server.
	// You can use this client to perform remote commands or file transfers.

	return nil
}

func LoadPrivateKey(path string) (ssh.Signer, error) {
	keyBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	privateKeyBlock, _ := pem.Decode(keyBytes)
	if privateKeyBlock == nil || privateKeyBlock.Type != "RSA PRIVATE KEY" {
		return nil, errors.New("failed to decode valid private key")
	}

	privateKey, err := x509.ParsePKCS1PrivateKey(privateKeyBlock.Bytes)
	if err != nil {
		return nil, err
	}

	return ssh.NewSignerFromKey(privateKey)
}

func GetPublicKey() (string, error) {
	publicKeyBytes, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return "", err
	}

	return string(publicKeyBytes), nil
}
