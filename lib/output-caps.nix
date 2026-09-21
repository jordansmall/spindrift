# Claude Code's own BASH_MAX_OUTPUT_LENGTH knob, baked into the image's
# config.Env by lib/image.nix. Hoisted here so the research-verdict prompt
# budget and the docs prose (issue #3669) derive the payload size from the
# same value the image bakes instead of hand-typing it again.
{
  bashMaxOutputLength = 8192;
}
