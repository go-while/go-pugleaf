echo "### Struct Signatures" > FuncStructList.md
find . -iname "*.go" -exec grep -nE "type.*struct\s{" {} + | sort >> FuncStructList.md
echo "### Function Signatures" >> FuncStructList.md
find . -iname "*.go" -exec grep -n "func (" {} + | sort >> FuncStructList.md


