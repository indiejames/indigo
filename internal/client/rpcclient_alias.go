package client

import (
	"crypto/sha256"
	"io"

	"github.com/indiejames/indigo/internal/debuglog"
	"github.com/indiejames/indigo/internal/rpcclient"
)

// The server connection lives in internal/rpcclient. It was split out of this
// package so it can be built without cgo: everything else here is the TUI,
// which imports internal/highlight and through it the tree-sitter C binding,
// and the MCP tools (internal/agenttools) need only the connection. Keeping
// that dependency out is what lets indigo-server — the static binary copied
// into dev containers — serve MCP as well (`indigo-server --mcp`).
//
// These aliases keep every existing client.X reference working unchanged.
// They are aliases, not new types, so a value crosses the package boundary
// with no conversion and methods defined in rpcclient are the same methods.

type (
	ActiveContext               = rpcclient.ActiveContext
	ActiveSelection             = rpcclient.ActiveSelection
	BufferConsistency           = rpcclient.BufferConsistency
	BufferStateReport           = rpcclient.BufferStateReport
	BufferSyncState             = rpcclient.BufferSyncState
	ClientBufferReport          = rpcclient.ClientBufferReport
	ClientCompletion            = rpcclient.ClientCompletion
	ClientDecoration            = rpcclient.ClientDecoration
	ClientDecorationKind        = rpcclient.ClientDecorationKind
	ClientDiag                  = rpcclient.ClientDiag
	ClientFixItem               = rpcclient.ClientFixItem
	ClientHoverResult           = rpcclient.ClientHoverResult
	ClientInlayHint             = rpcclient.ClientInlayHint
	ClientLocation              = rpcclient.ClientLocation
	ClientLspCodeAction         = rpcclient.ClientLspCodeAction
	ClientLspEdit               = rpcclient.ClientLspEdit
	ClientMenuItem              = rpcclient.ClientMenuItem
	ClientPluginBinding         = rpcclient.ClientPluginBinding
	ClientPopupItem             = rpcclient.ClientPopupItem
	ClientReference             = rpcclient.ClientReference
	ClientSemanticToken         = rpcclient.ClientSemanticToken
	ClientSigHelp               = rpcclient.ClientSigHelp
	ClientSigInfo               = rpcclient.ClientSigInfo
	ClientSigParam              = rpcclient.ClientSigParam
	ClientSymbol                = rpcclient.ClientSymbol
	ClientSyncState             = rpcclient.ClientSyncState
	ClientUnderlineStyle        = rpcclient.ClientUnderlineStyle
	ClientWorkspaceDiag         = rpcclient.ClientWorkspaceDiag
	DiagnosticsResult           = rpcclient.DiagnosticsResult
	DirEntry                    = rpcclient.DirEntry
	FileChangedMsg              = rpcclient.FileChangedMsg
	HideInputPromptMsg          = rpcclient.HideInputPromptMsg
	HidePluginPopupMsg          = rpcclient.HidePluginPopupMsg
	OpenFileAtMsg               = rpcclient.OpenFileAtMsg
	PluginDecorationsChangedMsg = rpcclient.PluginDecorationsChangedMsg
	PluginKeyResult             = rpcclient.PluginKeyResult
	PluginMoveCursorMsg         = rpcclient.PluginMoveCursorMsg
	PluginShowMsgMsg            = rpcclient.PluginShowMsgMsg
	RPC                         = rpcclient.RPC
	ReportBufferStateMsg        = rpcclient.ReportBufferStateMsg
	ServerDisconnectedMsg       = rpcclient.ServerDisconnectedMsg
	ShowInputPromptMsg          = rpcclient.ShowInputPromptMsg
	ShowPluginPopupMsg          = rpcclient.ShowPluginPopupMsg
	WorkspaceDiagnosticsResult  = rpcclient.WorkspaceDiagnosticsResult
	WorkspaceDiagnosticsSummary = rpcclient.WorkspaceDiagnosticsSummary
	WorkspaceEdit               = rpcclient.WorkspaceEdit
)

const (
	ClientDecorationGutter      = rpcclient.ClientDecorationGutter
	ClientDecorationLeftGutter  = rpcclient.ClientDecorationLeftGutter
	ClientDecorationLineTint    = rpcclient.ClientDecorationLineTint
	ClientDecorationOverlay     = rpcclient.ClientDecorationOverlay
	ClientDecorationRemovedLine = rpcclient.ClientDecorationRemovedLine
	ClientDecorationStatusBar   = rpcclient.ClientDecorationStatusBar
	ClientDecorationUnderline   = rpcclient.ClientDecorationUnderline
	ClientUnderlineCurly        = rpcclient.ClientUnderlineCurly
	ClientUnderlineNone         = rpcclient.ClientUnderlineNone
	ClientUnderlineStraight     = rpcclient.ClientUnderlineStraight
)

// Dial connects to the server listening on socketPath. See rpcclient.Dial.
func Dial(socketPath string) (*RPC, error) { return rpcclient.Dial(socketPath) }

// DialStream is Dial over an already-open stream. See rpcclient.DialStream.
func DialStream(rwc io.ReadWriteCloser) (*RPC, error) { return rpcclient.DialStream(rwc) }

func clientLog(format string, args ...any) {
	debuglog.Write("client", format, args...)
}

// BufferStateFor builds this model's answer to a consistency check.
func (m Model) BufferStateFor() BufferStateReport {
	sum := sha256.Sum256([]byte(m.buf.Content()))
	return BufferStateReport{
		Known:         true,
		Version:       m.version,
		Generation:    m.generation,
		Dirty:         m.buf.Dirty(),
		ContentSha256: sum[:],
	}
}
