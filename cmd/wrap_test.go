package cmd

import (
	"bytes"
	"io/ioutil"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/spf13/viper"
)

func TestWrap(t *testing.T) {
	Convey("wrap", t, func() {
		viper.Reset()
		viper.Set("kubeconfigfiles.directory", "/tmp/")

		Convey("returns error when no -e flag is set", func() {
			viper.Set("environments", map[string]interface{}{
				"edge-cmh": "kubeconfig-truss-nonprod-cmh",
			})

			cmd := rootCmd
			viper.BindPFlag("TRUSS_ENV", cmd.PersistentFlags().Lookup("env"))
			buff := bytes.NewBufferString("")
			cmd.SetOut(buff)
			cmd.SetArgs([]string{
				"wrap",
				"-e",
				"", // Simulating passing in no flag, here. I think persistent flag state is leaking between tests. TODO: Figure out a better integration testing strategy
				"--",
				"printenv",
			})
			cmd.Execute()
			out, _ := ioutil.ReadAll(buff)
			So(string(out), ShouldContainSubstring, "-e flag is required. Options: [edge-cmh]")
		})

		Convey("returns error when -e flag is set to an invalid environment", func() {
			viper.Set("environments", map[string]interface{}{
				"edge-cmh": "kubeconfig-truss-nonprod-cmh",
			})

			cmd := rootCmd
			viper.BindPFlag("TRUSS_ENV", cmd.PersistentFlags().Lookup("env"))
			buff := bytes.NewBufferString("")
			cmd.SetOut(buff)
			cmd.SetArgs([]string{
				"wrap",
				"-e",
				"noenv",
				"--",
				"printenv",
			})
			cmd.Execute()
			out, _ := ioutil.ReadAll(buff)
			So(string(out), ShouldContainSubstring, "unknown env noenv")
		})

		Convey("sets env from kubeconfings and runs command", func() {
			viper.Set("environments", map[string]interface{}{
				"edge-cmh": "kubeconfig-truss-nonprod-cmh",
			})

			cmd := rootCmd
			viper.BindPFlag("TRUSS_ENV", cmd.PersistentFlags().Lookup("env"))
			buff := bytes.NewBufferString("")
			cmd.SetOut(buff)
			cmd.SetArgs([]string{
				"wrap",
				"-e",
				"edge-cmh",
				"--",
				"printenv",
			})
			cmd.Execute()
			out, _ := ioutil.ReadAll(buff)
			So(string(out), ShouldContainSubstring, "KUBECONFIG=/tmp/kubeconfig-truss-nonprod-cmh")
		})
	})
}
