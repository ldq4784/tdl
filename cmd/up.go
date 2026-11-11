package cmd

import (
	"context"
	"errors"

	"github.com/gotd/td/telegram"
	"github.com/spf13/cobra"

	"github.com/iyear/tdl/app/up"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/storage"
)

func NewUpload() *cobra.Command {
	var opts up.Options
	var album bool

	cmd := &cobra.Command{
		Use:     "upload",
		Aliases: []string{"up"},
		Short:   "Upload anything to Telegram",
		GroupID: groupTools.ID,
		RunE: func(cmd *cobra.Command, args []string) error {
			return tRun(cmd.Context(), func(ctx context.Context, c *telegram.Client, kvd storage.Storage) error {
				if opts.Thread != 0 && opts.Chat == "" {
					return errors.New("error flags: --chat should be set when --topic is set")
				}
				if opts.Chat != "" && opts.To != "" {
					return errors.New("conflicting flags: --chat and --to cannot be set at the same time")
				}
				if album {
					opts.Group = true
				}
				if opts.GroupSize <= 0 || opts.GroupSize > 10 {
					opts.GroupSize = 10
				}
				return up.Run(logctx.Named(ctx, "up"), c, kvd, opts)
			})
		},
	}

	const (
		_chat   = "chat"
		path    = "path"
		include = "include"
		exclude = "exclude"
	)
	cmd.Flags().StringVarP(&opts.Chat, _chat, "c", "", "chat id or domain, and empty means 'Saved Messages'. Can be used together with --topic flag. Conflicts with --to flag.")
	cmd.Flags().IntVar(&opts.Thread, "topic", 0, "specify topic id. Must be used together with --chat flag. Conflicts with --to flag.")
	cmd.Flags().StringVar(&opts.To, "to", "", "destination peer, can be a CHAT or router based on expression engine. Conflicts with --chat and --topic flag.")
	cmd.Flags().StringSliceVarP(&opts.Paths, path, "p", []string{}, "dirs or files")
	cmd.Flags().StringSliceVarP(&opts.Includes, include, "i", []string{}, "include the specified file extensions")
	cmd.Flags().StringSliceVarP(&opts.Excludes, exclude, "e", []string{}, "exclude the specified file extensions")
	cmd.Flags().BoolVar(&opts.Remove, "rm", false, "remove the uploaded files after uploading")
	cmd.Flags().BoolVar(&opts.Photo, "photo", false, "upload the image as a photo instead of a file")
	cmd.Flags().BoolVar(&opts.Group, "group", false, "upload media files as a group/album (max 10 per group)")
	cmd.Flags().BoolVar(&album, "album", false, "upload media files as an album (alias for --group)")
	cmd.Flags().IntVar(&opts.GroupSize, "group-size", 10, "maximum number of media files per group/album (1-10)")
	cmd.Flags().StringVar(&opts.Caption, "caption", `"<code>"+FileName+"</code> - <code>"+MIME+"</code>"`, "caption for the uploaded media")

	// completion and validation
	_ = cmd.MarkFlagRequired(path)
	cmd.MarkFlagsMutuallyExclusive(include, exclude)

	return cmd
}
