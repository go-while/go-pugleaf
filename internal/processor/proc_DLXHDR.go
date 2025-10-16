package processor

import "github.com/go-while/go-pugleaf/internal/nntp"

// GetXHDR fetches XHDR data for a group
func (proc *Processor) GetXHDR(groupName string, header string, start, end int64) ([]nntp.HeaderLine, error) {
	// Fetch XHDR data from NNTP server
	xhdrData, err := proc.Pool.XHdr(groupName, header, start, end)
	if err != nil {
		return nil, err
	}
	return xhdrData, nil
}
