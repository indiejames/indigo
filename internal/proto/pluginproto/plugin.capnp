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

  # -- Editor effects --
  applyEdit    @5 (bufId :UInt32, edits :List(TextEdit))                  -> ();
  moveCursor   @6 (bufId :UInt32, pos :PluginPosition)                    -> ();
  openFile     @7 (path :Text, line :UInt32)                              -> ();
  showMessage  @8 (text :Text)                                            -> ();
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
  getDecorations @0 (bufId :UInt32, visibleRange :PluginRange) -> (decorations :List(Decoration));
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
  bufId    @0 :UInt32;
  mode     @1 :Text;
  clientId @2 :UInt64;
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
  line @0 :UInt32;
  col  @1 :UInt32;
  text @2 :Text;
  kind @3 :DecorationKind;
}

enum DecorationKind {
  gutter    @0;
  overlay   @1;
  statusBar @2;
}
