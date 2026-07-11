package bot

import (
	"bitbot/pb"
	"fmt"
	"strings"

	"github.com/charmbracelet/log"
)

// GenerateSSHKeyCore generates and saves a new SSH key pair.
func GenerateSSHKeyCore(regenerate bool) (string, error) {
	var err error
	if regenerate {
		err = GenerateAndSaveSSHKeyPair()
	} else {
		err = GenerateAndSaveSSHKeyPairIfNotExist()
	}

	if err != nil {
		log.Errorf("Error generating SSH key: %v", err)
		return "Error generating or saving key pair.", err
	}
	return "SSH key pair generated and saved successfully!", nil
}

// ShowSSHPublicKeyCore returns the current public SSH key.
func ShowSSHPublicKeyCore() (string, error) {
	publicKey, err := GetPublicKey()
	if err != nil {
		log.Errorf("Error fetching public SSH key: %v", err)
		return "Error fetching public key.", err
	}
	return publicKey, nil
}

// ConnectSSHServerCore connects to an SSH server and saves the connection.
func ConnectSSHServerCore(userID, guildID, connectionDetails string) (string, error) {
	sshConn, err := SSHConnectToRemoteServer(connectionDetails)
	if err != nil {
		log.Errorf("Error connecting to remote server %s: %v", connectionDetails, err)
		return fmt.Sprintf("Error connecting to remote server: %v", err), err
	}

	connectionKey := fmt.Sprintf("%s:%s", guildID, userID)
	sshConnections[connectionKey] = sshConn

	serverInfo := &pb.ServerInfo{UserID: userID, GuildID: guildID, ConnectionDetails: connectionDetails}
	err = pb.CreateRecord("servers", serverInfo)
	if err != nil {
		log.Errorf("Error saving server information to pb: %v", err)
		// We can still consider the connection successful even if saving fails.
		return "Connected to remote server! (Warning: Failed to save server to database)", nil
	}

	return "Connected to remote server and saved to database!", nil
}

// ExecuteSSHCommandCore executes a command on an active SSH connection.
func ExecuteSSHCommandCore(userID, guildID, command string) (string, error) {
	connectionKey := fmt.Sprintf("%s:%s", guildID, userID)
	sshConn, ok := sshConnections[connectionKey]
	if !ok {
		return "You are not connected to any remote server in this context. Please connect first.", fmt.Errorf("not connected")
	}

	response, err := sshConn.ExecuteCommand(command)
	if err != nil {
		log.Errorf("Error executing command '%s' on %s: %v", command, connectionKey, err)
		return fmt.Sprintf("Error executing command on remote server: %v", err), err
	}

	// If response is too long for discord/the LLM, we might need to truncate
	// but we'll let the caller or the LLM handle truncation for now.
	if response == "" {
		return "(Command executed successfully, no output)", nil
	}
	return response, nil
}

// CloseSSHConnectionCore closes an active SSH connection.
func CloseSSHConnectionCore(userID, guildID string) (string, error) {
	connectionKey := fmt.Sprintf("%s:%s", guildID, userID)
	sshConn, ok := sshConnections[connectionKey]
	if !ok {
		return "You are not connected to any remote server in this context.", fmt.Errorf("not connected")
	}

	sshConn.Close()
	delete(sshConnections, connectionKey)
	return "SSH connection closed.", nil
}

// ListSSHServersCore lists previously saved SSH servers for a user/guild.
func ListSSHServersCore(userID, guildID string) (string, error) {
	if guildID == "" {
		return "Listing servers is currently only supported within a server context.", fmt.Errorf("missing guildID")
	}

	servers, err := pb.ListServersByUserIDAndGuildID(userID, guildID)
	if err != nil {
		log.Errorf("Error listing servers for user %s in guild %s: %v", userID, guildID, err)
		return "Could not retrieve server list.", err
	}

	if len(servers) == 0 {
		return "You don't have any saved servers in this guild.", nil
	}

	var serverListMessage strings.Builder
	serverListMessage.WriteString("Saved servers in this guild:\n")
	for _, server := range servers {
		serverListMessage.WriteString(fmt.Sprintf("- `%s`\n", server.ConnectionDetails))
	}
	return serverListMessage.String(), nil
}
