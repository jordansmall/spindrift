# bench.sh's image-time arms. Each name becomes `.#ab-<name>`: the dogfood
# Harness with only the named roster entries' model and/or effort changed,
# for knobs baked into the image (worker/scout/filer models and every
# subagent's effort, #5254). Coordinator --model/--effort and
# --review-model/--review-effort are dispatch-time, so vary those in the
# bench arms file instead of adding an image here.
{
  variant = {
    models = {
      worker = "claude-haiku-5-5";
      reviewer = "claude-sonnet-5-5";
    };
  };
}
