package truss

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"

	"github.com/mitchellh/go-homedir"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v2"
)

// SecretsManager syncrhonizes secrets between the filesystem and Vault
type SecretsManager struct {
	*SecretConfigList
	VaultAuth VaultAuth
	Editor    string
}

// NewSecretsManager creates a new SecretsManager
func NewSecretsManager(editor string, vaultAuth VaultAuth) (*SecretsManager, error) {
	secretsFile := os.Getenv("TRUSS_SECRETS_FILE")
	if secretsFile == "" {
		secretsFile = "./secrets.yaml"
	}
	l, err := SecretConfigListFromFile(secretsFile)
	if err != nil {
		return nil, err
	}

	return &SecretsManager{
		SecretConfigList: l,
		Editor:           editor,
		VaultAuth:        vaultAuth,
	}, nil
}

// Edit edits an environments's secrets
// Returns true if $EDITOR wrote to the temp file
func (m SecretsManager) Edit(secret SecretConfig) (bool, error) {
	// start port-forward
	vault, err := m.Vault(secret)
	if err != nil {
		return false, err
	}
	if _, err = vault.PortForward(); err != nil {
		return false, err
	}
	defer vault.ClosePortForward()

	// load existing disk value
	// decrypt it or provide default
	raw, err := m.GetDecryptedFromDisk(vault, secret)
	if err != nil {
		return false, err
	}

	// save to tmp file
	tmpFile, err := ioutil.TempFile("", "trussvault-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Write(raw)
	tmpFile.Close()

	info, _ := os.Stat(tmpFile.Name())
	modTimeAtOpen := info.ModTime()

	// vim tmp file
	cmd := exec.Command(viper.GetString("EDITOR"), tmpFile.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Run()

	// check if saved
	info, _ = os.Stat(tmpFile.Name())
	modTimeAtClose := info.ModTime()
	if !modTimeAtClose.After(modTimeAtOpen) {
		return false, nil
	}

	// encrypt and save to disk
	raw, err = ioutil.ReadFile(tmpFile.Name())
	if err != nil {
		return true, err
	}
	if err := m.EncryptAndSaveToDisk(vault, secret, raw); err != nil {
		return true, err
	}

	return true, nil
}

// PushAll pushes all secrets for all environments
func (m SecretsManager) PushAll() error {
	for _, secret := range m.Secrets {
		if err := m.Push(secret); err != nil {
			return err
		}
	}
	return nil
}

// Push pushes secrets to Vaut
func (m SecretsManager) Push(secret SecretConfig) error {
	vault, err := m.Vault(secret)
	if err != nil {
		return err
	}
	if _, err := vault.PortForward(); err != nil {
		return err
	}
	defer vault.ClosePortForward()

	secrets, err := m.GetMapFromDisk(vault, secret)
	if err != nil {
		return err
	}

	for path, data := range secrets {
		m.Write(vault, secret, path, data)
	}
	return nil
}

// PullAll pulls all environments
func (m SecretsManager) PullAll() error {
	for _, secret := range m.Secrets {
		if err := m.Pull(secret); err != nil {
			return err
		}
	}
	return nil
}

// Pull updates the file on disk with the vaules from Vault (destructive)
func (m SecretsManager) Pull(secret SecretConfig) error {
	vault, err := m.Vault(secret)
	if err != nil {
		return err
	}
	if _, err := vault.PortForward(); err != nil {
		return err
	}
	defer vault.ClosePortForward()

	p, err := m.GetMapFromVault(vault, secret)
	if err != nil {
		return err
	}

	return m.WriteMapToDisk(vault, secret, p)
}

// Kubectl creates a Kubectl client
func (m SecretsManager) Kubectl(secret SecretConfig) (*KubectlCmd, error) {
	config := viper.GetStringMap("kubeconfigfiles")
	directory, ok := config["directory"].(string)
	if !ok {
		home, err := homedir.Dir()
		if err != nil {
			return nil, err
		}
		directory = home + "/.kube/"
	}

	return Kubectl(path.Join(directory, secret.Kubeconfig)), nil
}

// Vault creates a proxied Vault client
func (m SecretsManager) Vault(secret SecretConfig) (*VaultCmd, error) {
	kubectl, err := m.Kubectl(secret)
	if err != nil {
		return nil, err
	}

	return Vault(kubectl, m.VaultAuth), nil
}

// Exists determins whether the local secrets file exists
func (m SecretsManager) Exists(secret SecretConfig) bool {
	_, err := os.Stat(secret.FilePath)

	return err == nil
}

// GetDecryptedFromDisk returns the encrypted yaml configuration from disk
func (m SecretsManager) GetDecryptedFromDisk(vault *VaultCmd, secret SecretConfig) ([]byte, error) {
	if !m.Exists(secret) {
		return []byte("secrets: {}"), nil
	}

	encrypted, err := ioutil.ReadFile(secret.FilePath)
	if err != nil {
		return nil, err
	}

	return m.Decrypt(vault, encrypted)
}

// GetMapFromDisk returns a collection of secrets as a map
func (m SecretsManager) GetMapFromDisk(vault *VaultCmd, secret SecretConfig) (map[string]map[string]string, error) {
	raw, err := m.GetDecryptedFromDisk(vault, secret)
	if err != nil {
		return nil, err
	}

	p := struct {
		Secrets map[string]map[string]string `yaml:"secrets"`
	}{}
	if err := yaml.NewDecoder(bytes.NewReader(raw)).Decode(&p); err != nil {
		return nil, err
	}

	return p.Secrets, nil
}

// GetMapFromVault returns a collection of secrets as a map
func (m SecretsManager) GetMapFromVault(vault *VaultCmd, secret SecretConfig) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}

	list, err := vault.Run([]string{
		"kv",
		"list",
		"-format=yaml",
		secret.VaultPath,
	})
	if err != nil {
		return nil, err
	}

	secrets := []string{}
	if err := yaml.NewDecoder(bytes.NewReader(list)).Decode(&secrets); err != nil {
		return nil, err
	}

	for _, s := range secrets {
		get, err := vault.Run([]string{
			"kv",
			"get",
			"-format=yaml",
			path.Join(secret.VaultPath, s),
		})
		if err != nil {
			return nil, err
		}

		getData := struct {
			Data struct {
				Data map[string]string `yaml:"data"`
			} `yaml:"data"`
		}{}
		if err := yaml.NewDecoder(bytes.NewReader(get)).Decode(&getData); err != nil {
			return nil, err
		}

		out[s] = getData.Data.Data
	}

	return out, nil
}

// WriteMapToDisk serializes a collection of secrets and writes them encrypted to disk
func (m SecretsManager) WriteMapToDisk(vault *VaultCmd, secret SecretConfig, secrets map[string]map[string]string) error {
	s := map[string]map[string]map[string]string{
		"secrets": secrets,
	}

	y := bytes.NewBuffer(nil)
	if err := yaml.NewEncoder(y).Encode(s); err != nil {
		return err
	}

	enc, err := m.Encrypt(vault, y.Bytes())
	if err != nil {
		return err
	}

	return ioutil.WriteFile(secret.FilePath, enc, 0644)
}

// EncryptAndSaveToDisk encrypts and saves to disk
func (m SecretsManager) EncryptAndSaveToDisk(vault *VaultCmd, secret SecretConfig, raw []byte) error {
	enc, err := m.Encrypt(vault, raw)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(secret.FilePath, enc, 0644)
}

// Decrypt shit
func (m SecretsManager) Decrypt(vault *VaultCmd, encrypted []byte) ([]byte, error) {
	out, err := vault.Run([]string{
		"write",
		"-field=plaintext",
		"transit/decrypt/" + m.TransitKeyName,
		"ciphertext=" + string(encrypted),
	})

	if err != nil {
		return nil, err
	}

	return base64.StdEncoding.DecodeString(string(out))
}

// Encrypt shit
func (m SecretsManager) Encrypt(vault *VaultCmd, raw []byte) ([]byte, error) {
	out, err := vault.Run([]string{
		"write",
		"-field=ciphertext",
		"transit/encrypt/" + m.TransitKeyName,
		"plaintext=" + base64.StdEncoding.EncodeToString(raw),
	})

	if err != nil {
		return nil, err
	}

	return out, nil
}

// Write writes a secret to Vault
func (m SecretsManager) Write(vault *VaultCmd, secret SecretConfig, dst string, data map[string]string) error {
	args := []string{"kv", "put", path.Join(secret.VaultPath, dst)}
	for k, v := range data {
		args = append(args, fmt.Sprintf("%s=%s", k, v))
	}

	_, err := vault.Run(args)
	return err
}
