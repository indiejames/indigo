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
  bufferInfo   @14 (bufId :UInt32)                                        -> (path :Text, languageId :Text, lineCount :UInt32, isDirty :Bool);
  visibleRange @15 (clientId :UInt64)                                     -> (startLine :UInt32, endLine :UInt32);
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
