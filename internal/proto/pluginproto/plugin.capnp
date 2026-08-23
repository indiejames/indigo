using Go = import "/go.capnp";
@0xa3f8c1e2d4b07659;
$Go.package("pluginproto");
$Go.import("github.com/indiejames/indigo/internal/proto/pluginproto");

# Plugin is the interface a plugin process must implement.
# The server calls initialize once at startup; the plugin registers
# all its capabilities via the EditorApi it receives.
interface Plugin {
  initialize @0 (api :EditorApi) -> (info :PluginInfo);
}

# EditorApi is the capability the server exposes to each plugin.
# Registration methods are called during initialize.
# Effect and query methods may be called at any time.
interface EditorApi {
  # -- Registration --
  registerKeyBinding    @0 (trigger :Text, handler :KeyHandler)           -> ();
  registerInsertHook    @1 (char :Text,    handler :KeyHandler)           -> ();
  registerCommand       @2 (name :Text,    handler :CommandHandler)       -> ();
  registerBufferHandler @3 (handler :BufferEventHandler)                  -> ();
  registerDecorations   @4 (provider :DecorationProvider)                 -> ();
  registerActionProvider @16 (provider :ActionProvider)                    -> ();
  # registerMenuAction registers a handler invoked when the user selects this
  # plugin's item in the Command (space) menu. Unlike registerKeyBinding, the
  # handler is never bound to a physical key — id is an opaque string chosen
  # by the plugin and referenced by the "command" field of its plugin.toml
  # menu_item entries. The handler receives the same KeyContext/KeyResponse
  # shape as a normal-mode key handler (mode is always "normal").
  registerMenuAction @20 (id :Text, handler :KeyHandler)                   -> ();
  # registerCompletionProvider registers a source of completion candidates for
  # this plugin, merged into the same list the editor's LSP-driven completions
  # populate (see docs/plugin-architecture.md). At most one provider per plugin;
  # a later call replaces an earlier one.
  registerCompletionProvider @22 (provider :CompletionProvider)           -> ();

  # -- Plugin-driven UI --
  # showPopup displays an interactive list; the handler is called when the user
  # selects an item (with the item's opaque data field) or cancels.
  showPopup          @17 (title :Text, items :List(PopupItem), handler :PopupHandler)         -> ();
  # registerEditHandler receives a linesChanged call after every edit that changes
  # the line count, letting the plugin track line-indexed data (e.g. bookmarks).
  registerEditHandler @18 (handler :EditEventHandler)                                          -> ();
  # showInputPrompt shows a single-line text input dialog; handler is called on
  # confirm (with the entered text) or cancel.
  showInputPrompt    @19 (title :Text, placeholder :Text, handler :InputPromptHandler)         -> ();

  # -- Editor effects --
  applyEdit    @5 (bufId :UInt32, edits :List(TextEdit))                  -> ();
  moveCursor   @6 (bufId :UInt32, pos :PluginPosition)                    -> ();
  openFile     @7 (path :Text, line :UInt32)                              -> ();
  # clientId 0 broadcasts to all clients; non-zero targets one client.
  showMessage  @8 (clientId :UInt64, text :Text)                          -> ();
  runProcess   @9 (cmd :Text, args :List(Text))                           -> (stdout :Text, stderr :Text, exitCode :Int32);

  # -- Document model queries --
  readBuffer   @10 (bufId :UInt32)                                        -> (content :Text);
  readLines    @11 (bufId :UInt32, startLine :UInt32, endLine :UInt32)    -> (lines :List(Text));
  readRange    @12 (bufId :UInt32, from :PluginPosition, to :PluginPosition) -> (text :Text);
  wordAt       @13 (bufId :UInt32, pos :PluginPosition)                   -> (start :PluginPosition, end :PluginPosition, found :Bool);
  bufferInfo   @14 (bufId :UInt32)                                        -> (path :Text, languageId :Text, lineCount :UInt32, isDirty :Bool, version :UInt64);
  visibleRange @15 (clientId :UInt64)                                     -> (startLine :UInt32, endLine :UInt32);
  # refreshDecorations tells any client currently viewing bufId to refetch
  # decorations now, instead of waiting for its next poll tick. Call this
  # after async work (an LLM completion, a git blame fetch, ...) finishes
  # and would change what DecorationProvider.getDecorations returns —
  # fire-and-forget, does not itself carry the new decorations.
  refreshDecorations @21 (bufId :UInt32)                                  -> ();
  # publishDiagnostics reports this plugin's diagnostics for bufId, computed
  # against version (the buffer's version at the time the plugin computed
  # them — see bufferInfo). The server discards the call if version doesn't
  # match the buffer's current version, rather than overwriting live
  # diagnostics with stale ones computed against old content; the plugin
  # should just recompute and call again. An empty diagnostics list clears
  # this plugin's previously published diagnostics for bufId. Diagnostics
  # are cached server-side and merged into the same list LSP/lint
  # diagnostics populate (status bar counts, the diagnostics popup, gutter
  # markers) — unlike DecorationProvider, there is no pull/poll counterpart,
  # this call is the only way plugin diagnostics reach the editor.
  publishDiagnostics @23 (bufId :UInt32, version :UInt64, diagnostics :List(PluginDiagnostic)) -> ();
  # publishWorkspaceDiagnostics is the path-keyed sibling of publishDiagnostics,
  # for a file that may not be open in any buffer (no bufId/version exists for
  # one). Called from a WorkspaceScanHandler.scan callback (see
  # registerWorkspaceScanHandler) while walking the project. Like
  # publishDiagnostics, one call is the authoritative diagnostic list for
  # (this plugin, path) — a later call replaces the previous one entirely,
  # and an empty diagnostics list clears it. There is no staleness check
  # here (nothing to compare against for an unopened file); an open buffer's
  # live diagnostics always supersede whatever was published here for the
  # same path, never merge alongside it — see allWorkspaceDiagnostics.
  publishWorkspaceDiagnostics @24 (path :Text, diagnostics :List(PluginDiagnostic)) -> ();
  # registerWorkspaceScanHandler registers a handler invoked when the editor
  # runs (or re-runs) a workspace-wide diagnostic scan — at server startup
  # and on an explicit rescan (the diagnostic browser's "r" key), mirroring
  # lint.Manager.ScanWorkspace's own trigger points. The handler should walk
  # the project (e.g. every text file under the workspace root) and call
  # publishWorkspaceDiagnostics per file as it goes; it is dispatched
  # fire-and-forget with a generous timeout (scanning a whole project can
  # take a while), so slow progress does not block the RescanWorkspaceDiagnostics
  # RPC or any other plugin's scan.
  registerWorkspaceScanHandler @25 (handler :WorkspaceScanHandler) -> ();
}

# Handler interfaces — implemented by the plugin, called by the server.

interface KeyHandler {
  handle @0 (key :Text, ctx :KeyContext) -> (response :KeyResponse);
}

interface CommandHandler {
  handle @0 (args :List(Text), ctx :CommandContext) -> ();
}

interface BufferEventHandler {
  onOpen   @0 (event :BufferEvent) -> ();
  onChange @1 (event :BufferEvent) -> ();
  onSave   @2 (event :BufferEvent) -> ();
  onClose  @3 (event :BufferEvent) -> ();
}

interface DecorationProvider {
  getDecorations @0 (bufId :UInt32, visibleRange :PluginRange, clientId :UInt64) -> (decorations :List(Decoration));
  # getFixes returns fix options for a fixable decoration identified by fixData.
  # The editor calls this when the user triggers the fix action (Shift+F) on a
  # decoration that has fixable = true.
  getFixes       @1 (fixData :Text) -> (items :List(FixItem));
  # applyFix is called for FixItems whose replace field is empty (custom actions).
  applyFix       @2 (fixData :Text, index :UInt32) -> ();
}

# ActionProvider supplies context-sensitive actions for any cursor position.
# Unlike DecorationProvider.getFixes, actions are not tied to a visible decoration.
interface ActionProvider {
  # getActions is called when the user presses F; returns actions relevant to (bufId, line, col).
  getActions  @0 (bufId :UInt32, line :UInt32, col :UInt32) -> (items :List(ActionItem));
  # applyAction is called for ActionItems whose replace field is empty (custom actions).
  applyAction @1 (bufId :UInt32, line :UInt32, col :UInt32, index :UInt32) -> ();
}

# ActionItem is one option presented in the F popup from an ActionProvider.
struct ActionItem {
  label    @0 :Text;
  replace  @1 :Text;    # non-empty = insert this text after deleting [from, to)
  fromLine @2 :UInt32;
  fromCol  @3 :UInt32;
  toLine   @4 :UInt32;
  toCol    @5 :UInt32;
}

# CompletionProvider supplies completion candidates for a cursor position,
# merged with the buffer's language-server completions into one list.
interface CompletionProvider {
  # getCompletions returns candidates for (bufId, line, col). Called on every
  # completion request while the popup is open, so it should return quickly;
  # anything slow (a registry lookup, a network call) belongs behind
  # resolveCompletion instead, triggered only for the item the user is about
  # to accept.
  getCompletions    @0 (bufId :UInt32, line :UInt32, col :UInt32) -> (items :List(CompletionItem));
  # resolveCompletion is called with the exact item returned by getCompletions
  # (including its opaque data token) when the user is about to accept it,
  # to fill in anything left out for speed (e.g. detail/documentation).
  # Return the item unchanged if there's nothing to add.
  resolveCompletion @1 (item :CompletionItem)                     -> (item :CompletionItem);
}

# CompletionItem is one candidate from a CompletionProvider.
struct CompletionItem {
  label      @0 :Text;
  kind       @1 :UInt8;  # same LSP CompletionItemKind values the editor's own items use
  detail     @2 :Text;
  insertText @3 :Text;   # plain-text insert at the cursor; ignored when textEdit is set
  # sortText/filterText drive client-side ranking/filtering; filterText falls
  # back to label when empty.
  sortText   @4 :Text;
  filterText @5 :Text;
  # textEdit, when present, is the authoritative replace range for accepting
  # this item (e.g. replacing an entire partially-typed version string, not
  # just inserting at the cursor). Preferred over insertText.
  textEdit   @6 :TextEdit;
  # data is an opaque token round-tripped to resolveCompletion unchanged, for
  # the plugin to identify which candidate is being resolved.
  data       @7 :Text;
}

# -- Supporting structs --

struct PluginInfo {
  name    @0 :Text;
  version @1 :Text;
}

struct PluginPosition {
  line @0 :UInt32;
  col  @1 :UInt32;
}

struct PluginRange {
  start @0 :PluginPosition;
  end   @1 :PluginPosition;
}

enum PluginDiagnosticSeverity {
  error   @0;
  warning @1;
  info    @2;
  hint    @3;
}

# PluginDiagnostic is one diagnostic reported via publishDiagnostics. The
# server stamps the publishing plugin's name onto it as the diagnostic's
# Source before merging with LSP/lint diagnostics, so there is no source
# field here for the plugin to set itself.
struct PluginDiagnostic {
  range    @0 :PluginRange;
  severity @1 :PluginDiagnosticSeverity;
  message  @2 :Text;
}

struct TextEdit {
  from    @0 :PluginPosition;
  to      @1 :PluginPosition;
  newText @2 :Text;
}

struct KeyContext {
  bufId      @0 :UInt32;
  mode       @1 :Text;
  clientId   @2 :UInt64;
  cursorLine @3 :UInt32;
  cursorCol  @4 :UInt32;
}

struct KeyResponse {
  handled     @0 :Bool;
  edits       @1 :List(TextEdit);
  cursorPos   @2 :PluginPosition;
  hasCursor   @3 :Bool;
  captureKeys @4 :UInt32; # >0 = capture this many more keypresses in "capture" mode
}

struct CommandContext {
  bufId @0 :UInt32;
}

struct BufferEvent {
  bufId @0 :UInt32;
  path  @1 :Text;
}

struct Decoration {
  line           @0 :UInt32;
  col            @1 :UInt32;
  text           @2 :Text;
  kind           @3 :DecorationKind;
  endCol         @4 :UInt32;          # end column (exclusive) for underline spans
  underlineStyle @5 :UnderlineStyle;  # only meaningful when kind = underline
  underlineColor @6 :Text;            # hex color e.g. "#FF8C00"; empty = default
  fixable        @7 :Bool;            # true = Shift+F can offer fixes here
  fixData        @8 :Text;            # opaque token passed back to getFixes/applyFix
  textColor      @9 :Text;            # hex foreground color for gutter/overlay text; empty = default
}

# FixItem is one option presented to the user in the fix popup.
# If replace is non-empty, the editor applies it directly as a text replacement.
# If replace is empty, the editor calls applyFix with the item's index.
struct FixItem {
  label   @0 :Text;
  replace @1 :Text;
}

enum DecorationKind {
  gutter     @0;
  overlay    @1;
  statusBar  @2;
  underline  @3; # applies underlineStyle/underlineColor to the span [col, endCol)
  leftGutter @4; # 2-cell left gutter slot (left of line numbers); text = single char
}

enum UnderlineStyle {
  none     @0;
  straight @1;
  curly    @2; # undercurl / wavy — terminals that don't support it show straight
}

# PopupItem is one entry in a plugin-driven list popup.
struct PopupItem {
  label    @0 :Text;  # primary label (shown prominently)
  sublabel @1 :Text;  # secondary label (dimmed, shown to the right)
  data     @2 :Text;  # opaque token returned to the plugin on selection
}

# PopupHandler is implemented by the plugin and called by the editor when the
# user interacts with a popup opened via showPopup.
interface PopupHandler {
  selected  @0 (data :Text) -> ();  # user picked an item; data = item.data
  cancelled @1 ()           -> ();  # user dismissed without selecting
}

# InputPromptHandler is implemented by the plugin and called by the editor when
# the user interacts with a prompt opened via showInputPrompt.
interface InputPromptHandler {
  confirmed @0 (text :Text) -> ();  # user confirmed; text = entered value
  cancelled @1 ()           -> ();  # user pressed Esc
}

# EditEventHandler is implemented by the plugin and called after every edit
# that changes the document's line count, so the plugin can keep line-indexed
# data (such as bookmark positions) in sync.
interface EditEventHandler {
  # linesChanged is fired after an edit at atLine that shifted the line count
  # by lineDelta (positive = lines inserted, negative = lines deleted).
  linesChanged @0 (bufId :UInt32, filePath :Text, atLine :UInt32, lineDelta :Int32) -> ();
}

# WorkspaceScanHandler is implemented by the plugin and called by the editor
# to run a workspace-wide diagnostic scan — see registerWorkspaceScanHandler.
interface WorkspaceScanHandler {
  scan @0 () -> ();
}
