echo "### Struct Signatures" > FuncStructList.txt
find . -iname "*.go" -exec grep -nE "type.*struct\s{" {} + | sort >> FuncStructList.txt
echo "### Function Signatures" >> FuncStructList.txt
find . -iname "*.go" -exec grep -n "func (" {} + | sort >> FuncStructList.txt


