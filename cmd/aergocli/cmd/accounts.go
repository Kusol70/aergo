package cmd

import (
	"context"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/crypto/ssh/terminal"

	"github.com/aergoio/aergo/account/key"
	"github.com/aergoio/aergo/cmd/aergocli/util"
	"github.com/aergoio/aergo/types"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
)

func init() {
	rootCmd.AddCommand(newAccountCmd)
	newAccountCmd.Flags().StringVar(&pw, "password", "", "password")
	newAccountCmd.Flags().BoolVar(&remote, "remote", true, "choose account in the remote node or not")
	newAccountCmd.Flags().StringVar(&dataDir, "path", "$HOME/.aergo/data", "path to data directory")
	rootCmd.AddCommand(getAccountsCmd)
	getAccountsCmd.Flags().StringVar(&pw, "password", "", "password")
	getAccountsCmd.Flags().BoolVar(&remote, "remote", true, "choose account in the remote node or not")
	getAccountsCmd.Flags().StringVar(&dataDir, "path", "$HOME/.aergo/data", "path to data directory")
	rootCmd.AddCommand(unlockAccountCmd)
	unlockAccountCmd.Flags().StringVar(&address, "address", "", "address of account")
	unlockAccountCmd.MarkFlagRequired("address")
	unlockAccountCmd.Flags().StringVar(&pw, "password", "", "password")
	unlockAccountCmd.MarkFlagRequired("password")
	rootCmd.AddCommand(lockAccountCmd)
	lockAccountCmd.Flags().StringVar(&address, "address", "", "address of account")
	lockAccountCmd.MarkFlagRequired("address")
	lockAccountCmd.Flags().StringVar(&pw, "password", "", "password")
	lockAccountCmd.MarkFlagRequired("password")
}

var pw string
var remote bool
var dataDir string
var newAccountCmd = &cobra.Command{
	Use:     "newaccount",
	Short:   "Create new account in the node or cli",
	PreRun:  preConnectAergo,
	PostRun: disconnectAergo,
	Run: func(cmd *cobra.Command, args []string) {
		var param types.Personal
		var err error
		if pw != "" {
			param.Passphrase = pw
		} else {
			param.Passphrase, err = getPasswd()
			if err != nil {
				fmt.Printf("Failed: %s\n", err.Error())
				return
			}
		}
		var msg *types.Account
		var addr []byte
		if remote {
			msg, err = client.CreateAccount(context.Background(), &param)
		} else {
			dataEnvPath := os.ExpandEnv(dataDir)
			ks := key.NewStore(dataEnvPath)
			addr, err = ks.CreateKey(param.Passphrase)
			if nil != err {
				fmt.Printf("Failed: %s\n", err.Error())
			}
			err = ks.SaveAddress(addr)
		}
		if nil != err {
			fmt.Printf("Failed: %s\n", err.Error())
		} else {
			if msg != nil {
				fmt.Println(types.EncodeAddress(msg.GetAddress()))
			} else {
				fmt.Println(types.EncodeAddress(addr))
			}
		}
	},
}

var getAccountsCmd = &cobra.Command{
	Use:     "getaccounts",
	Short:   "Get account list in the node or cli",
	PreRun:  preConnectAergo,
	PostRun: disconnectAergo,
	Run: func(cmd *cobra.Command, args []string) {

		var err error
		var msg *types.AccountList
		var addrs [][]byte
		if remote {
			serverAddr := GetServerAddress()
			opts := []grpc.DialOption{grpc.WithInsecure()}
			var client *util.ConnClient
			var ok bool
			if client, ok = util.GetClient(serverAddr, opts).(*util.ConnClient); !ok {
				panic("Internal error. wrong RPC client type")
			}
			defer client.Close()

			msg, err = client.GetAccounts(context.Background(), &types.Empty{})
		} else {
			dataEnvPath := os.ExpandEnv(dataDir)
			ks := key.NewStore(dataEnvPath)
			addrs, err = ks.GetAddresses()
		}
		if nil == err {
			out := fmt.Sprintf("%s", "[")
			if msg != nil {
				addresslist := msg.GetAccounts()
				for _, a := range addresslist {
					out = fmt.Sprintf("%s%s, ", out, types.EncodeAddress(a.Address))
				}
				if addresslist != nil {
					out = out[:len(out)-2]
				}
			} else if addrs != nil {
				for _, a := range addrs {
					out = fmt.Sprintf("%s%s, ", out, types.EncodeAddress(a))
				}
				out = out[:len(out)-2]
			}
			out = fmt.Sprintf("%s%s", out, "]")
			fmt.Println(out)
		} else {
			fmt.Printf("Failed: %s\n", err.Error())
		}
	},
}

var lockAccountCmd = &cobra.Command{
	Use:               "lockaccount",
	Short:             "Lock account in the node",
	PersistentPreRun:  connectAergo,
	PersistentPostRun: disconnectAergo,
	Run: func(cmd *cobra.Command, args []string) {

		param, err := parsePersonalParam()
		if err != nil {
			return
		}
		msg, err := client.LockAccount(context.Background(), param)
		if err == nil {
			fmt.Println(types.EncodeAddress(msg.GetAddress()))
		} else {
			fmt.Printf("Failed: %s\n", err.Error())
		}
	},
}

var unlockAccountCmd = &cobra.Command{
	Use:               "unlockaccount",
	Short:             "Unlock account in the node",
	PersistentPreRun:  connectAergo,
	PersistentPostRun: disconnectAergo,
	Run: func(cmd *cobra.Command, args []string) {
		param, err := parsePersonalParam()
		if err != nil {
			return
		}
		msg, err := client.UnlockAccount(context.Background(), param)
		if nil == err {
			fmt.Println(types.EncodeAddress(msg.GetAddress()))
		} else {
			fmt.Printf("Failed: %s\n", err.Error())
		}
	},
}

func parsePersonalParam() (*types.Personal, error) {
	var err error
	param := &types.Personal{Account: &types.Account{}}
	if address != "" {
		param.Account.Address, err = types.DecodeAddress(address)
		if err != nil {
			fmt.Printf("Failed: %s\n", err.Error())
			return nil, err
		}
		if pw != "" {
			param.Passphrase = pw
		} else {
			param.Passphrase, err = getPasswd()
			if err != nil {
				fmt.Printf("Failed: %s\n", err.Error())
				return nil, err
			}
		}
	}
	return param, nil
}

func getPasswd() (string, error) {
	fmt.Print("Enter Password: ")
	bytePassword, err := terminal.ReadPassword(int(syscall.Stdin))
	fmt.Println("")
	return string(bytePassword), err
}

func preConnectAergo(cmd *cobra.Command, args []string) {
	if remote {
		connectAergo(cmd, args)
	}
}
