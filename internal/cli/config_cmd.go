package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/azazo1/download-cli/internal/config"
)

func newConfigCmd() *cobra.Command {
	var path string
	cmd := &cobra.Command{
		Use:   "config",
		Short: "查看或初始化配置文件",
	}
	cmd.PersistentFlags().StringVar(&path, "config", "", "配置文件路径")
	resolved := func() string {
		if path != "" {
			return path
		}
		return config.DefaultPath()
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "输出配置文件路径",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), resolved())
		},
	})

	var force bool
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "写入默认配置文件",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := resolved()
			if err := config.WriteDefault(p, force); err != nil {
				return usageErr("%v", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "已写入", p)
			return nil
		},
	}
	initCmd.Flags().BoolVar(&force, "force", false, "覆盖已存在的配置文件")
	cmd.AddCommand(initCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "输出合并默认值后的生效配置",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, existed, err := config.Load(resolved())
			if err != nil {
				return usageErr("%v", err)
			}
			data, err := config.Encode(cfg)
			if err != nil {
				return err
			}
			if !existed {
				fmt.Fprintln(cmd.OutOrStdout(), "# 配置文件不存在, 以下为内置默认值")
			}
			fmt.Fprint(cmd.OutOrStdout(), string(data))
			return nil
		},
	})
	return cmd
}

func newVersionCmd(version string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "输出版本号",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), "dn", version)
		},
	}
}
