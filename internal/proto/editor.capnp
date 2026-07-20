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
}

interface EditorService {
  connect         @0 (callback :ClientCallback)                                -> (clientId :UInt64);
  disconnect      @1 (clientId :UInt64)                                        -> ();
  openFile        @2 (clientId :UInt64, path :Text)                            -> (bufferId :UInt32, content :Text, version :UInt64, fromRecovery :Bool);
  # savedHash is the sha256 of the buffer content at its last save; clients
  # compare it against their own content hash to keep dirty markers accurate
  # when another client (e.g. an agent) saves the buffer.
  getUpdates      @3 (clientId :UInt64, bufferId :UInt32, sinceVersion :UInt64) -> (ops :List(EditOp), version :UInt64, savedHash :Data);
  applyOp         @4 (clientId :UInt64, bufferId :UInt32, op :EditOp)          -> (version :UInt64);
  save            @5 (clientId :UInt64, bufferId :UInt32)                      -> ();
  closeBuffer     @6 (clientId :UInt64, bufferId :UInt32)                      -> ();
  bufferClientCount @7 (bufferId :UInt32)                                      -> (count :UInt32);
  discardRecovery @8 (clientId :UInt64, bufferId :UInt32)                      -> (content :Text);
  getDiagnostics  @9  (bufId :UInt32)                                          -> (items :List(LspDiagnostic), lspReady :Bool);
  hover           @10 (bufId :UInt32, line :UInt32, col :UInt32)               -> (result :HoverResult);
  signatureHelp   @11 (bufId :UInt32, line :UInt32, col :UInt32)               -> (result :SignatureHelp);
  complete        @12 (bufId :UInt32, line :UInt32, col :UInt32)               -> (items :List(CompletionItem));
  definition      @13 (bufId :UInt32, line :UInt32, col :UInt32)               -> (result :DefinitionResult);
  format          @14 (bufId :UInt32)                                           -> (content :Text, changed :Bool, noFormatter :Bool);
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
}

enum PluginDecorationKind {
  gutter     @0;
  overlay    @1;
  statusBar  @2;
  underline  @3;
  leftGutter @4;
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
  textColor      @10 :Text;  # hex foreground color for gutter/overlay text; empty = default
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
