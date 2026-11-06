# The output shown is replayed from a real run. To regenerate:
#   make build && examples/demo/capture.sh
#   docker run --rm -v "$PWD:/vhs" ghcr.io/charmbracelet/vhs examples/demo/demo.tape
Output examples/demo/demo.gif

Set Shell bash
Set FontSize 16
Set Width 1280
Set Height 560
Set Padding 16
Set TypingSpeed 35ms
Set Theme "Catppuccin Mocha"

Hide
Type "source examples/demo/replay.sh"
Enter
Sleep 500ms
Show

Sleep 500ms
Type "whatdies check 'kubectl delete ns shop'"
Enter
Sleep 4s

Type 'kubectl patch pv "$PV" -p "{\"spec\":{\"persistentVolumeReclaimPolicy\":\"Retain\"}}"'
Enter
Sleep 1s

Type "whatdies check 'kubectl delete ns shop'"
Enter
Sleep 4s

Type `whatdies hook --codex <<< '{"tool_name":"Bash","tool_input":{"command":"kubectl delete ns shop"}}'; echo "exit $?"`
Enter
Sleep 5s

Hide
Type "clear"
Enter
Sleep 300ms
Show

Type "whatdies check 'helm uninstall blog -n blog'"
Enter
Sleep 4s

Type "whatdies check 'terraform apply -auto-approve'"
Enter
Sleep 4s

Type "whatdies check 'terraform apply tfplan'"
Enter
Sleep 3s

Type "whatdies check 'terraform apply bucket.tfplan'"
Enter
Sleep 4s
