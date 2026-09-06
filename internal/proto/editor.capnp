using Go = import "/go.capnp";
@0xd75b3b54eb3ed6a2;
$Go.package("proto");
$Go.import("github.com/indiejames/indigo/internal/proto");

# EditorService is the capability exposed by the server over a Unix socket.
interface ClientCallback {
  showMessage     @0 (text :Text)                                -> ();
  moveCursor      @1 (bufId :UInt32, line :UInt32, col :UInt32) -> ();
  openFile        @2 (path :Text, line :UInt32)                  -> ();
  keyRegistered   @3 (trigger :Text)                             -> ();
  fileChanged     @4 (bufId :UInt32, dirty :Bool)                -> ();
  showPluginPopup @5 (title :Text, items :List(PopupItem))       -> ();
  hidePluginPopup @6 ()                                          -> ();
  showInputPrompt @7 (title :Text, placeholder :Text)            -> ();
  hideInputPrompt @8 ()                                          -> ();
  # Mirrors keyRegistered but for OnInsert hooks: handles the race where a
  # plugin registers an insert hook after this client's initial
  # getPluginInsertChars snapshot was taken.
  insertHookRegistered @9 (char :Text)                           -> ();
  # Sent when a plugin calls the SDK's RefreshDecorations(bufId) — tells the
  # client to refetch decorations for bufId now rather than on the next poll
  # tick. A client not currently viewing bufId ignores it.
  decorationsChanged   @10 (bufId :UInt32)                       -> ();
}

interface EditorService {
  connect         @0 (callback :ClientCallback)                                -> (clientId :UInt64);
  disconnect      @1 (clientId :UInt64)                                        -> ();
  # generation increments every time the server replaces this buffer's
  # underlying object wholesale (format-on-save, SaveAs, DiscardRecovery,
  # explicit Format) rather than applying an incremental Op — version alone
  # can't signal this, since a fresh buffer object always starts at 0.
  openFile        @2 (clientId :UInt64, path :Text)                            -> (bufferId :UInt32, content :Text, version :UInt64, fromRecovery :Bool, generation :UInt64);
  # savedHash is the sha256 of the buffer content at its last save; clients
  # compare it against their own content hash to keep dirty markers accurate
  # when another client (e.g. an agent) saves the buffer.
  # generation: see openFile's doc comment. A client whose remembered
  # generation doesn't match this must discard ops and do a full resync
  # (e.g. via getBufferSnapshot) instead of applying them — they describe
  # changes to a different buffer object than the one it has locally.
  getUpdates      @3 (clientId :UInt64, bufferId :UInt32, sinceVersion :UInt64) -> (ops :List(EditOp), version :UInt64, savedHash :Data, generation :UInt64);
  # getBufferSnapshot fetches a buffer's current authoritative content by
  # ID rather than path — used to resync after a failed ApplyOp or a
  # detected generation mismatch. Path lookup (openFile) can't be used for
  # this: the client's own remembered path may itself be stale if a
  # *different* client renamed the buffer via SaveAs since this one last
  # synced, which would make openFile spuriously create a second buffer
  # for the (possibly now-nonexistent) old path instead of finding the
  # existing one.
  getBufferSnapshot @49 (bufferId :UInt32) -> (content :Text, version :UInt64, generation :UInt64, path :Text);
  # generation must match the buffer's current generation (see openFile's
  # doc comment) or the op is rejected — a client unaware of a wholesale
  # buffer swap must not have its (now-meaningless) coordinates applied to
  # the new buffer object. The client should resync (getBufferSnapshot) on
  # rejection rather than retry.
  applyOp         @4 (clientId :UInt64, bufferId :UInt32, op :EditOp, generation :UInt64) -> (version :UInt64);
  save            @5 (clientId :UInt64, bufferId :UInt32)                      -> ();
  closeBuffer     @6 (clientId :UInt64, bufferId :UInt32)                      -> ();
  bufferClientCount @7 (bufferId :UInt32)                                      -> (count :UInt32);
  discardRecovery @8 (clientId :UInt64, bufferId :UInt32)                      -> (content :Text);
  getDiagnostics  @9  (bufId :UInt32)                                          -> (items :List(LspDiagnostic), lspReady :Bool);
  hover           @10 (bufId :UInt32, line :UInt32, col :UInt32)               -> (result :HoverResult);
  signatureHelp   @11 (bufId :UInt32, line :UInt32, col :UInt32)               -> (result :SignatureHelp);
  complete        @12 (bufId :UInt32, line :UInt32, col :UInt32)               -> (items :List(CompletionItem));
  definition      @13 (bufId :UInt32, line :UInt32, col :UInt32)               -> (result :DefinitionResult);
  # generation: see openFile's doc comment — Format is one of the sites
  # that wholesale-swaps the buffer object when it makes a change, so a
  # caller must adopt this into its own remembered generation the same way
  # it would after openFile/getUpdates/getBufferSnapshot, or its very next
  # getUpdates poll will (correctly, but confusingly for this specific
  # case — it was this same client's own edit) see a mismatch and trigger
  # an unnecessary resync. Only meaningful when changed is true.
  format          @14 (bufId :UInt32)                                           -> (content :Text, changed :Bool, noFormatter :Bool, generation :UInt64);
  handlePluginKey      @15 (clientId :UInt64, bufId :UInt32, key :Text, mode :Text, cursorLine :UInt32, cursorCol :UInt32) -> (result :PluginKeyResult);
  updateViewport       @16 (clientId :UInt64, topLine :UInt32, height :UInt32)      -> ();
  getPluginDecorations @17 (clientId :UInt64, bufId :UInt32)                        -> (decorations :List(PluginDecoration));
  getPluginKeys        @18 ()                                                        -> (keys :List(Text));
  getPluginBindings    @24 ()                                                        -> (bindings :List(PluginBinding));
  saveAs               @19 (clientId :UInt64, bufferId :UInt32, path :Text)          -> ();
  getPluginFixes       @20 (pluginName :Text, fixData :Text)                         -> (items :List(PluginFixItem));
  applyPluginFix       @21 (pluginName :Text, fixData :Text, index :UInt32)          -> ();
  getPluginActions     @22 (bufId :UInt32, line :UInt32, col :UInt32)                -> (items :List(PluginActionItem));
  applyPluginAction    @23 (pluginName :Text, bufId :UInt32, line :UInt32, col :UInt32, index :UInt32) -> ();
  pluginPopupSelected  @25 (index :UInt32)  -> ();
  pluginPopupCancelled @26 ()               -> ();
  pluginInputConfirmed @27 (text :Text)     -> ();
  pluginInputCancelled @28 ()               -> ();
  references       @29 (bufId :UInt32, line :UInt32, col :UInt32) -> (locations :List(FileLocation));
  workspaceSymbols @30 (bufId :UInt32, query :Text)               -> (symbols :List(SymbolResult));
  documentSymbols  @31 (bufId :UInt32)                            -> (symbols :List(SymbolResult));
  # line/col and endLine/endCol give the request range: equal for a plain
  # cursor position, or the ordered bounds of an active selection so the
  # language server can offer range-only actions like Extract Function/
  # Extract Variable in addition to point-based quick-fixes.
  lspCodeActions     @32 (bufId :UInt32, line :UInt32, col :UInt32, endLine :UInt32, endCol :UInt32) -> (actions :List(LspCodeAction));
  setActiveContext   @33 (clientId :UInt64, bufId :UInt32, filePath :Text, line :UInt32, col :UInt32) -> ();
  getActiveContext   @34 () -> (result :ActiveContext);
  setStatusBarText   @35 (key :Text, text :Text) -> ();
  # Apply a batch of ops atomically: once the server receives the call, all
  # ops are applied even if the client dies mid-request.
  applyOps           @36 (clientId :UInt64, bufferId :UInt32, ops :List(EditOp)) -> (version :UInt64);
  # Report / query the active editor selection (start/end in document order,
  # end column inclusive; isLine = whole-line selection; active=false clears).
  setActiveSelection @37 (clientId :UInt64, bufId :UInt32, startLine :UInt32, startCol :UInt32, endLine :UInt32, endCol :UInt32, isLine :Bool, active :Bool) -> ();
  getActiveSelection @38 () -> (bufId :UInt32, startLine :UInt32, startCol :UInt32, endLine :UInt32, endCol :UInt32, isLine :Bool, found :Bool);
  # Apply a batch of workspace-wide search-and-replace edits, one file write
  # (or shared-buffer update) per affected path. Each edit's oldText is
  # verified against the file's current content immediately before applying
  # it; edits whose oldText no longer matches (concurrent change since the
  # edit was queued) are skipped and reported back via skippedIdx rather than
  # applied blindly. Paths that already have a shared open buffer are edited
  # in place and left dirty (not saved); paths with no open buffer are
  # patched directly on disk.
  applyWorkspaceEdits @39 (clientId :UInt64, edits :List(WorkspaceEdit)) -> (appliedCount :UInt32, skippedIdx :List(UInt32));
  # Pushes an open-file command (0-based line) to every connected client —
  # the same broadcast Go-native plugins get via ServerBridge.PluginOpenFile,
  # exposed over the wire so external clients (e.g. the indigo-claude plugin
  # process) can ask the user's editor window(s) to navigate somewhere.
  requestOpenFile     @40 (path :Text, line :UInt32) -> ();
  # getMenuItems returns the declarative Command-menu (space menu) tree
  # contributed by plugin manifests (plugin.toml menu_item entries).
  getMenuItems        @41 ()                                                        -> (items :List(MenuItemInfo));
  # invokePluginMenuAction dispatches a Command-menu selection to the plugin
  # that registered it via registerMenuAction, identified by pluginName+command.
  # Reuses PluginKeyResult exactly like handlePluginKey.
  invokePluginMenuAction @42 (clientId :UInt64, bufId :UInt32, pluginName :Text, command :Text, cursorLine :UInt32, cursorCol :UInt32) -> (result :PluginKeyResult);
  # Renames the symbol at (line, col) via the language server, applying the
  # resulting edits across every affected file (same apply semantics as
  # applyWorkspaceEdits: a path with a shared open buffer is edited in place,
  # a path with no open buffer is patched on disk). appliedCount and
  # fileCount are both 0 if there's no language server for this buffer, or
  # it doesn't support renaming, or there was nothing to rename.
  lspRename @43 (clientId :UInt64, bufId :UInt32, line :UInt32, col :UInt32, newName :Text) -> (appliedCount :UInt32, fileCount :UInt32);
  # Moves the text in [fromLine,fromCol, toLine,toCol) (end exclusive) out of
  # bufId and appends it to destPath, creating the file if it doesn't exist.
  # destPath with a shared open buffer is edited in place; otherwise it's
  # patched directly on disk (same semantics as applyWorkspaceEdits). No
  # attempt is made to fix up imports or other cross-file references — the
  # caller is responsible for that.
  moveTextToFile @44 (clientId :UInt64, bufId :UInt32, fromLine :UInt32, fromCol :UInt32, toLine :UInt32, toCol :UInt32, destPath :Text) -> ();
  # Resolves a completion item returned by `complete`, filling in the lazily
  # computed additionalTextEdits (the auto-import line for a symbol imported
  # from another module). The item's data blob must be passed back unchanged
  # so the language server can identify which candidate to resolve.
  resolveCompletion @45 (bufId :UInt32, item :CompletionItem) -> (item :CompletionItem);
  # Inlay hints (inferred types, parameter names) for [startLine,endLine) —
  # normally the client's visible viewport, not the whole file.
  inlayHints @46 (bufId :UInt32, startLine :UInt32, startCol :UInt32, endLine :UInt32, endCol :UInt32) -> (hints :List(InlayHint));
  # Semantic tokens (LSP-derived syntax coloring) for [startLine,endLine) —
  # normally the client's visible viewport, not the whole file.
  semanticTokensRange @47 (bufId :UInt32, startLine :UInt32, startCol :UInt32, endLine :UInt32, endCol :UInt32) -> (tokens :List(SemanticToken));
  # Chars with a registered OnInsert hook (any plugin), fetched once at
  # connect time — mirrors getPluginKeys. See insertHookRegistered for the
  # live-update counterpart covering hooks registered after connect.
  getPluginInsertChars @48 () -> (chars :List(Text));
  # Requests "source.organizeImports" from bufId's language server over the
  # whole file (sorts/dedupes/adds-missing/removes-unused imports, per that
  # server's own rules — e.g. gopls, typescript-language-server,
  # rust-analyzer all implement it). Returns the edits for the client to
  # apply through the normal undo-aware batch path, same as lspCodeActions;
  # empty means no language server, no organize-imports support, or nothing
  # to change.
  lspOrganizeImports @50 (bufId :UInt32) -> (edits :List(PluginEdit));
  # getWorkspaceDiagnostics aggregates diagnostics across the whole
  # project, tagged with each one's file path — unlike getDiagnostics,
  # which is scoped to a single bufId. Open buffers get live LSP + lint +
  # plugin diagnostics (same sources as getDiagnostics); files that aren't
  # open in any buffer get whatever the last workspace scan found for them
  # (see rescanWorkspaceDiagnostics) — a whole-project lint invocation, a
  # plugin's OnWorkspaceScan handler, and (best-effort, where the server
  # both is already running and advertised workspace/diagnostic pull
  # support) LSP diagnostics via lsp.Manager.ScanWorkspace. An LSP server
  # that was never started this session (nothing of its language opened
  # yet) or that doesn't advertise workspace/diagnostic contributes
  # nothing for unopened files of its language — a documented, permanent
  # limitation rather than a gap to close later.
  # truncated is true if the result was capped (see maxWorkspaceDiagnostics
  # in server_lsp.go) rather than exhaustive.
  getWorkspaceDiagnostics @51 () -> (items :List(WorkspaceDiagnosticItem), truncated :Bool);
  # getWorkspaceDiagnosticsSummary is the cheap counts-only counterpart to
  # getWorkspaceDiagnostics, for a workspace-wide status indicator that
  # doesn't need the full list. fileCount is the number of distinct files
  # (open buffers plus workspace-scanned files) with at least one
  # diagnostic.
  getWorkspaceDiagnosticsSummary @52 () -> (errorCount :UInt32, warningCount :UInt32, infoCount :UInt32, fileCount :UInt32);
  # rescanWorkspaceDiagnostics triggers async whole-project scans to
  # refresh diagnostics for files that aren't open in any buffer, across
  # every source that supports one: lint (internal/lint.Manager.ScanWorkspace),
  # LSP (internal/lsp.Manager.ScanWorkspace — best-effort/partial, see its
  # doc comment), and any plugin with an OnWorkspaceScan handler
  # (pluginMgr.DispatchWorkspaceScan). Fire-and-forget: this returns as soon
  # as the scans are (re)started, not once they complete — results show up
  # through the next getWorkspaceDiagnostics/Summary call. A scan already in
  # progress for a given source coalesces this into one more run right
  # after it finishes.
  rescanWorkspaceDiagnostics @53 () -> ();
}

# MenuItemInfo is one node in the Command-menu tree contributed by a plugin.
# A leaf node has a non-empty command (the id passed to registerMenuAction);
# a group node has children and an empty command.
struct MenuItemInfo {
  label      @0 :Text;
  key        @1 :Text;  # suggested single-char selector within its parent menu
  pluginName @2 :Text;
  command    @3 :Text;
  children   @4 :List(MenuItemInfo);
}

struct WorkspaceEdit {
  path    @0 :Text;
  line    @1 :UInt32;
  col     @2 :UInt32;
  oldText @3 :Text;
  newText @4 :Text;
}

struct PluginEdit {
  fromLine @0 :UInt32;
  fromCol  @1 :UInt32;
  toLine   @2 :UInt32;
  toCol    @3 :UInt32;
  newText  @4 :Text;
}

struct LspCodeAction {
  title       @0 :Text;
  edits       @1 :List(PluginEdit);
  # kind is the LSP CodeActionKind (e.g. "refactor.extract.function"). Used
  # client-side to detect range-extract refactors that introduce a new,
  # not-yet-named symbol, so the client can prompt for a name and rename it
  # immediately via a real LSP rename instead of leaving gopls's default
  # ("newFunction"/"newVar") in place.
  kind        @2 :Text;
}

enum PluginDecorationKind {
  gutter      @0;
  overlay     @1;
  statusBar   @2;
  underline   @3;
  leftGutter  @4;
  removedLine @5; # extra screen row above `line`; [col,endCol) = intra-line emphasis
  lineTint    @6; # background behind [col,endCol) of `line`; textColor = background hex
}

struct PopupItem {
  label    @0 :Text;
  sublabel @1 :Text;
  data     @2 :Text;
}

enum PluginUnderlineStyle {
  none     @0;
  straight @1;
  curly    @2;
}

struct PluginDecoration {
  line           @0 :UInt32;
  col            @1 :UInt32;
  text           @2 :Text;
  kind           @3 :PluginDecorationKind;
  endCol         @4 :UInt32;
  underlineStyle @5 :PluginUnderlineStyle;
  underlineColor @6 :Text;
  fixable        @7 :Bool;
  fixData        @8 :Text;
  pluginName     @9 :Text;
  textColor      @10 :Text;  # hex colour: foreground for gutter/overlay, background for lineTint
  oldLine        @11 :UInt32; # for removedLine: this content's line number in the pre-change file
}

struct PluginFixItem {
  label   @0 :Text;
  replace @1 :Text;
}

struct PluginBinding {
  pluginName  @0 :Text;
  key         @1 :Text;
  description @2 :Text;
}

struct PluginActionItem {
  label      @0 :Text;
  replace    @1 :Text;
  fromLine   @2 :UInt32;
  fromCol    @3 :UInt32;
  toLine     @4 :UInt32;
  toCol      @5 :UInt32;
  pluginName @6 :Text;
}

struct PluginKeyResult {
  handled     @0 :Bool;
  edits       @1 :List(PluginEdit);
  cursorLine  @2 :UInt32;
  cursorCol   @3 :UInt32;
  hasCursor   @4 :Bool;
  captureKeys @5 :UInt32; # >0 = client should send next N keys to the plugin as "capture" mode
}

struct DefinitionResult {
  found @0 :Bool;
  path  @1 :Text;
  line  @2 :UInt32;
  col   @3 :UInt32;
}

struct FileLocation {
  path    @0 :Text;
  line    @1 :UInt32;
  col     @2 :UInt32;
  preview @3 :Text;
}

struct SymbolResult {
  name          @0 :Text;
  kind          @1 :UInt8;
  containerName @2 :Text;
  path          @3 :Text;
  line          @4 :UInt32;
  col           @5 :UInt32;
}

struct LspDiagnostic {
  line     @0 :UInt32;
  col      @1 :UInt32;
  endLine  @2 :UInt32;
  endCol   @3 :UInt32;
  severity @4 :UInt8;
  message  @5 :Text;
  source   @6 :Text;
}

# WorkspaceDiagnosticItem is one diagnostic from getWorkspaceDiagnostics,
# the same shape as LspDiagnostic plus the path it belongs to (getDiagnostics
# omits path since it's already scoped to one bufId's caller-known path).
struct WorkspaceDiagnosticItem {
  path     @0 :Text;
  line     @1 :UInt32;
  col      @2 :UInt32;
  endLine  @3 :UInt32;
  endCol   @4 :UInt32;
  severity @5 :UInt8;
  message  @6 :Text;
  source   @7 :Text;
}

struct HoverResult {
  contents @0 :Text;
  found    @1 :Bool;
}

struct SignatureParam {
  label @0 :Text;
}

struct SignatureInfo {
  label           @0 :Text;
  documentation   @1 :Text;
  parameters      @2 :List(SignatureParam);
  activeParameter @3 :UInt32;
}

struct SignatureHelp {
  signatures      @0 :List(SignatureInfo);
  activeSignature @1 :UInt32;
  activeParameter @2 :UInt32;
  found           @3 :Bool;
}

struct CompletionItem {
  label      @0 :Text;
  kind       @1 :UInt8;
  detail     @2 :Text;
  insertText @3 :Text;
  # data is the opaque resolve token from the language server, sent back
  # unchanged via resolveCompletion to obtain additionalTextEdits. Empty when
  # the server provides no resolve data.
  data                @4 :Data;
  # additionalTextEdits are edits to apply elsewhere in the document when the
  # item is accepted (the auto-import line). Empty in `complete` results;
  # populated by resolveCompletion.
  additionalTextEdits @5 :List(PluginEdit);
  # sortText/filterText drive client-side ranking and filtering. filterText
  # falls back to label when empty.
  sortText   @6 :Text;
  filterText @7 :Text;
  # textEdit, when present (HasTextEdit), is the authoritative primary edit for
  # accepting this item (its range may cover more than the typed prefix, e.g.
  # the whole identifier when completing mid-word). Preferred over insertText.
  textEdit @8 :PluginEdit;
  # source is empty for a language-server-provided item, or the name of the
  # plugin that supplied it (see CompletionProvider in pluginproto/plugin.capnp).
  # resolveCompletion uses this to route the resolve call to the right place.
  source @9 :Text;
}

struct SemanticToken {
  line      @0 :UInt32;
  col       @1 :UInt32;  # already converted from UTF-16 to a rune index server-side
  length    @2 :UInt32;  # in runes, same conversion applied
  tokenType @3 :Text;    # already resolved from the server's legend
  modifiers @4 :List(Text);
}

struct InlayHint {
  line         @0 :UInt32;
  col          @1 :UInt32;
  label        @2 :Text;  # already normalized to a plain string server-side
  kind         @3 :UInt8; # 1 = Type, 2 = Parameter
  paddingLeft  @4 :Bool;
  paddingRight @5 :Bool;
}

struct ActiveContext {
  clientId  @0 :UInt64;
  bufId     @1 :UInt32;
  filePath  @2 :Text;
  line      @3 :UInt32;
  col       @4 :UInt32;
  updatedAt @5 :Int64;  # Unix nanoseconds
  found     @6 :Bool;
}

struct EditOp {
  clientId  @0 :UInt64;
  version   @1 :UInt64;
  type      @2 :OpType;

  # Insert fields
  insertLine @3 :UInt32;
  insertCol  @4 :UInt32;
  insertText @5 :Text;

  # Delete fields
  fromLine @6 :UInt32;
  fromCol  @7 :UInt32;
  toLine   @8 :UInt32;
  toCol    @9 :UInt32;

  enum OpType {
    noop   @0;
    insert @1;
    delete @2;
  }
}
