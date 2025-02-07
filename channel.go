package disgord

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/andersfylling/disgord/internal/endpoint"
	"github.com/andersfylling/disgord/internal/httd"
	"github.com/andersfylling/disgord/json"
)

// ChannelType https://discord.com/developers/docs/resources/channel#channel-object-channel-types
type ChannelType uint

const (
	ChannelTypeGuildText ChannelType = iota
	ChannelTypeDM
	ChannelTypeGuildVoice
	ChannelTypeGroupDM
	ChannelTypeGuildCategory
	ChannelTypeGuildNews
	ChannelTypeGuildStore
	_
	_
	_
	ChannelTypeGuildNewsThread
	ChannelTypeGuildPublicThread
	// a temporary sub-channel within a GUILD_TEXT channel that is only viewable by those
	// invited and those with the MANAGE_THREADS permission
	ChannelTypeGuildPrivateThread
)

// VideoQualityMode https://discord.com/developers/docs/resources/channel#channel-object-video-quality-modes
type VideoQualityMode uint

const (
	VideoQualityModeAuto VideoQualityMode = 1
	VideoQualityModeFull VideoQualityMode = 2
)

// Attachment https://discord.com/developers/docs/resources/channel#attachment-object
type Attachment struct {
	ID       Snowflake `json:"id"`
	Filename string    `json:"filename"`
	Size     uint      `json:"size"`
	URL      string    `json:"url"`
	ProxyURL string    `json:"proxy_url"`
	Height   uint      `json:"height"`
	Width    uint      `json:"width"`

	SpoilerTag bool `json:"-"`
}

var _ internalUpdater = (*Attachment)(nil)
var _ Copier = (*Attachment)(nil)
var _ DeepCopier = (*Attachment)(nil)

func (a *Attachment) updateInternals() {
	a.SpoilerTag = strings.HasPrefix(a.Filename, AttachmentSpoilerPrefix)
}

type PermissionOverwriteType uint8

const (
	PermissionOverwriteRole PermissionOverwriteType = iota
	PermissionOverwriteMember
)

// PermissionOverwrite https://discord.com/developers/docs/resources/channel#overwrite-object
//
// WARNING! Discord is bugged, and the Type field needs to be a string to read Permission Overwrites from audit log
type PermissionOverwrite struct {
	ID    Snowflake               `json:"id"` // role or user id
	Type  PermissionOverwriteType `json:"type"`
	Allow PermissionBit           `json:"allow"`
	Deny  PermissionBit           `json:"deny"`
}

// type ChannelDeleter interface { DeleteChannel(id Snowflake) (err error) }
// type ChannelUpdater interface {}

// PartialChannel ...
// example of partial channel
// // "channel": {
// //   "id": "165176875973476352",
// //   "name": "illuminati",
// //   "type": 0
// // }
type PartialChannel struct {
	ID   Snowflake   `json:"id"`
	Name string      `json:"name"`
	Type ChannelType `json:"type"`
}

// Channel ...
type Channel struct {
	ID                         Snowflake             `json:"id"`
	Type                       ChannelType           `json:"type"`
	GuildID                    Snowflake             `json:"guild_id,omitempty"`
	Position                   int                   `json:"position,omitempty"` // can be less than 0
	PermissionOverwrites       []PermissionOverwrite `json:"permission_overwrites,omitempty"`
	Name                       string                `json:"name,omitempty"`
	Topic                      string                `json:"topic,omitempty"`
	NSFW                       bool                  `json:"nsfw,omitempty"`
	LastMessageID              Snowflake             `json:"last_message_id,omitempty"`
	Bitrate                    uint                  `json:"bitrate,omitempty"`
	UserLimit                  uint                  `json:"user_limit,omitempty"`
	RateLimitPerUser           uint                  `json:"rate_limit_per_user,omitempty"`
	Recipients                 []*User               `json:"recipients,omitempty"` // empty if not DM/GroupDM
	Icon                       string                `json:"icon,omitempty"`
	OwnerID                    Snowflake             `json:"owner_id,omitempty"`
	ApplicationID              Snowflake             `json:"application_id,omitempty"`
	ParentID                   Snowflake             `json:"parent_id,omitempty"`
	LastPinTimestamp           Time                  `json:"last_pin_timestamp,omitempty"`
	MessageCount               int                   `json:"message_count,omitempty"`                 //threads only. stops counting at 50
	MemberCount                int                   `json:"member_count,omitempty"`                  //threads only. stops counting at 50
	ThreadMetadata             ThreadMetadata        `json:"thread_metadata,omitempty"`               //threads only
	Member                     ThreadMember          `json:"member,omitempty"`                        //threads only
	DefaultAutoArchiveDuration int                   `json:"default_auto_archive_duration,omitempty"` //threads only
}

var _ Reseter = (*Channel)(nil)
var _ fmt.Stringer = (*Channel)(nil)
var _ Copier = (*Channel)(nil)
var _ DeepCopier = (*Channel)(nil)
var _ Mentioner = (*Channel)(nil)

func (c *Channel) String() string {
	return "channel{name:'" + c.Name + "', id:" + c.ID.String() + "}"
}

func (c *Channel) valid() bool {
	if c.RateLimitPerUser > 120 {
		return false
	}

	if len(c.Topic) > 1024 {
		return false
	}

	if c.Name != "" && (len(c.Name) > 100 || len(c.Name) < 2) {
		return false
	}

	return true
}

// GetPermissions is used to get a members permissions in a channel.
func (c *Channel) GetPermissions(ctx context.Context, s GuildQueryBuilderCaller, member *Member) (permissions PermissionBit, err error) {
	// Get the guild permissions.
	permissions, err = member.GetPermissions(ctx, s)
	if err != nil {
		return 0, err
	}

	// Handle permission overwrites.
	apply := func(o PermissionOverwrite) {
		permissions |= o.Allow
		permissions &= (-o.Deny) - 1
	}
	for _, overwrite := range c.PermissionOverwrites {
		if overwrite.Type == PermissionOverwriteMember {
			// This is a member. Is it me?
			if overwrite.ID == member.UserID {
				// It is! Time to apply the overwrites.
				apply(overwrite)
			}
			continue
		}

		for _, role := range member.Roles {
			if role == overwrite.ID {
				apply(overwrite)
				break
			}
		}
	}

	// Return the result.
	return
}

// Mention creates a channel mention string. Mention format is according the Discord protocol.
func (c *Channel) Mention() string {
	return "<#" + c.ID.String() + ">"
}

// Compare checks if channel A is the same as channel B
func (c *Channel) Compare(other *Channel) bool {
	// eh
	return (c == nil && other == nil) || (other != nil && c.ID == other.ID)
}

// SendMsgString same as SendMsg, however this only takes the message content (string) as a argument for the message
func (c *Channel) SendMsgString(ctx context.Context, s Session, content string) (msg *Message, err error) {
	if c.ID.IsZero() {
		err = newErrorMissingSnowflake("snowflake ID not set for channel")
		return
	}
	params := &CreateMessage{
		Content: content,
	}

	msg, err = s.Channel(c.ID).WithContext(ctx).CreateMessage(params)
	return
}

// SendMsg sends a message to a channel
func (c *Channel) SendMsg(ctx context.Context, s Session, message *Message) (msg *Message, err error) {
	if c.ID.IsZero() {
		err = newErrorMissingSnowflake("snowflake ID not set for channel")
		return
	}
	nonce := fmt.Sprint(message.Nonce)
	if len(nonce) > 25 {
		return nil, errors.New("nonce can not be longer than 25 characters")
	}

	params := &CreateMessage{
		Content:          message.Content,
		Nonce:            nonce, // THIS IS A STRING. NOT A SNOWFLAKE! DONT TOUCH!
		Tts:              message.Tts,
		MessageReference: message.MessageReference,
		// File: ...
		// Embed: ...
	}
	if len(message.Embeds) > 0 {
		params.Embed = message.Embeds[0]
	}

	msg, err = s.Channel(c.ID).WithContext(ctx).CreateMessage(params)
	return
}

//////////////////////////////////////////////////////
//
// REST Methods
//
//////////////////////////////////////////////////////

func (c clientQueryBuilder) Channel(id Snowflake) ChannelQueryBuilder {
	return &channelQueryBuilder{client: c.client, cid: id}
}

// ChannelQueryBuilder REST interface for all Channel endpoints
type ChannelQueryBuilder interface {
	WithContext(ctx context.Context) ChannelQueryBuilder
	WithFlags(flags ...Flag) ChannelQueryBuilder

	// TriggerTypingIndicator Post a typing indicator for the specified channel. Generally bots should not implement
	// this route. However, if a bot is responding to a command and expects the computation to take a few seconds, this
	// endpoint may be called to let the user know that the bot is processing their message. Returns a 204 empty response
	// on success. Fires a Typing Start Gateway event.
	TriggerTypingIndicator() error

	// Get Get a channel by Snowflake. Returns a channel object.
	Get() (*Channel, error)

	// Update a Channels settings. Requires the 'MANAGE_CHANNELS' permission for the guild. Returns
	// a channel on success, and a 400 BAD REQUEST on invalid parameters. Fires a Channel Update Gateway event. If
	// modifying a category, individual Channel Update events will fire for each child channel that also changes.
	// For the PATCH method, all the JSON Params are optional.
	Update(params *UpdateChannel) (*Channel, error)

	// Delete a channel, or close a private message. Requires the 'MANAGE_CHANNELS' permission for
	// the guild. Deleting a category does not delete its child Channels; they will have their parent_id removed and a
	// Channel Update Gateway event will fire for each of them. Returns a channel object on success.
	// Fires a Channel Delete Gateway event.
	Delete() (*Channel, error)

	// UpdatePermissions Edit the channel permission overwrites for a user or role in a channel. Only usable
	// for guild Channels. Requires the 'MANAGE_ROLES' permission. Returns a 204 empty response on success.
	// For more information about permissions, see permissions.
	UpdatePermissions(overwriteID Snowflake, params *UpdateChannelPermissions) error

	// GetInvites Returns a list of invite objects (with invite metadata) for the channel. Only usable for
	// guild Channels. Requires the 'MANAGE_CHANNELS' permission.
	GetInvites() ([]*Invite, error)

	// CreateInvite Create a new invite object for the channel. Only usable for guild Channels. Requires
	// the CREATE_INSTANT_INVITE permission. All JSON parameters for this route are optional, however the request
	// body is not. If you are not sending any fields, you still have to send an empty JSON object ({}).
	// Returns an invite object.
	CreateInvite(params *CreateInvite) (*Invite, error)

	// DeletePermission Delete a channel permission overwrite for a user or role in a channel. Only usable
	// for guild Channels. Requires the 'MANAGE_ROLES' permission. Returns a 204 empty response on success. For more
	// information about permissions,
	// see permissions: https://discord.com/developers/docs/topics/permissions#permissions
	DeletePermission(overwriteID Snowflake) error

	// AddDMParticipant Adds a recipient to a Group DM using their access token. Returns a 204 empty response
	// on success.
	AddDMParticipant(participant *GroupDMParticipant) error

	// KickParticipant Removes a recipient from a Group DM. Returns a 204 empty response on success.
	KickParticipant(userID Snowflake) error

	// GetPinnedMessages Returns all pinned messages in the channel as an array of message objects.
	GetPinnedMessages() ([]*Message, error)

	// DeleteMessages Delete multiple messages in a single request. This endpoint can only be used on guild
	// Channels and requires the 'MANAGE_MESSAGES' permission. Returns a 204 empty response on success. Fires multiple
	// Message Delete Gateway events.Any message IDs given that do not exist or are invalid will count towards
	// the minimum and maximum message count (currently 2 and 100 respectively). Additionally, duplicated IDs
	// will only be counted once.
	DeleteMessages(params *DeleteMessages) error

	// GetMessages Returns the messages for a channel. If operating on a guild channel, this endpoint requires
	// the 'VIEW_CHANNEL' permission to be present on the current user. If the current user is missing
	// the 'READ_MESSAGE_HISTORY' permission in the channel then this will return no messages
	// (since they cannot read the message history). Returns an array of message objects on success.
	GetMessages(params *GetMessages) ([]*Message, error)

	// CreateMessage Post a message to a guild text or DM channel. If operating on a guild channel, this
	// endpoint requires the 'SEND_MESSAGES' permission to be present on the current user. If the tts field is set to true,
	// the SEND_TTS_MESSAGES permission is required for the message to be spoken. Returns a message object. Fires a
	// Message Create Gateway event. See message formatting for more information on how to properly format messages.
	// The maximum request size when sending a message is 8MB.
	CreateMessage(params *CreateMessage) (*Message, error)

	// CreateWebhook Create a new webhook. Requires the 'MANAGE_WEBHOOKS' permission.
	// Returns a webhook object on success.
	CreateWebhook(params *CreateWebhook) (ret *Webhook, err error)

	// GetWebhooks Returns a list of channel webhook objects. Requires the 'MANAGE_WEBHOOKS' permission.
	GetWebhooks() (ret []*Webhook, err error)

	Message(id Snowflake) MessageQueryBuilder

	// CreateThread Create a thread that is not connected to an existing message.
	CreateThread(params *CreateThreadWithoutMessage) (*Channel, error)

	// JoinThread Adds the current user to a thread. Also requires the thread is not archived.
	// Returns a 204 empty response on success.
	JoinThread() error

	// AddThreadMember Adds another member to a thread. Requires the ability to send messages in the thread.
	// Also requires the thread is not archived. Returns a 204 empty response if the member
	// is successfully added or was already a member of the thread.
	AddThreadMember(userID Snowflake) error

	// LeaveThread Removes the current user from a thread. Also requires the thread is not archived.
	// Returns a 204 empty response on success.
	LeaveThread() error

	// RemoveThreadMember Removes another member from a thread. Requires the MANAGE_THREADS permission, or the
	// creator of the thread if it is a GUILD_PRIVATE_THREAD. Also requires the thread is not archived.
	// Returns a 204 empty response on success.
	RemoveThreadMember(userID Snowflake) error

	// GetThreadMember Returns a thread member object for the specified user if
	// they are a member of the thread, returns a 404 response otherwise.
	GetThreadMember(userID Snowflake) (*ThreadMember, error)

	// GetThreadMembers Returns array of thread members objects that are members of the thread.
	// This endpoint is restricted according to whether the GUILD_MEMBERS Privileged Intent is enabled for your application.
	GetThreadMembers() ([]*ThreadMember, error)

	// GetPublicArchivedThreads Returns archived threads in the channel that are public. When called on a GUILD_TEXT channel, returns
	// threads of type GUILD_PUBLIC_THREAD. When called on a GUILD_NEWS channel returns threads of type
	// GUILD_NEWS_THREAD. Threads are ordered by archive_timestamp, in descending order. Requires the READ_MESSAGE_HISTORY
	// permission.
	GetPublicArchivedThreads(params *GetArchivedThreads) (*ArchivedThreads, error)

	// GetPrivateArchivedThreads Returns archived threads in the channel that are of type GUILD_PRIVATE_THREAD. Threads are ordered by
	// archive_timestamp, in descending order. Requires both the READ_MESSAGE_HISTORY and MANAGE_THREADS permissions.
	GetPrivateArchivedThreads(params *GetArchivedThreads) (*ArchivedThreads, error)

	// GetJoinedPrivateArchivedThreads Returns archived threads in the channel that are of type GUILD_PRIVATE_THREAD, and the user has joined.
	// Threads are ordered by their id, in descending order. Requires the READ_MESSAGE_HISTORY permission.
	GetJoinedPrivateArchivedThreads(params *GetArchivedThreads) (*ArchivedThreads, error)
}

type channelQueryBuilder struct {
	ctx    context.Context
	flags  Flag
	client *Client
	cid    Snowflake
}

var _ ChannelQueryBuilder = (*channelQueryBuilder)(nil)

func (c *channelQueryBuilder) validate() error {
	if c.client == nil {
		return ErrMissingClientInstance
	}
	if c.cid.IsZero() {
		return ErrMissingChannelID
	}
	return nil
}

func (c channelQueryBuilder) WithContext(ctx context.Context) ChannelQueryBuilder {
	c.ctx = ctx
	return &c
}

func (c channelQueryBuilder) WithFlags(flags ...Flag) ChannelQueryBuilder {
	c.flags = mergeFlags(flags)
	return &c
}

// Get [REST] Get a channel by Snowflake. Returns a channel object.
//
//	Method                  GET
//	Endpoint                /channels/{channel.id}
//	Discord documentation   https://discord.com/developers/docs/resources/channel#get-channel
//	Reviewed                2018-06-07
//	Comment                 -
func (c channelQueryBuilder) Get() (*Channel, error) {
	if c.cid.IsZero() {
		return nil, ErrMissingChannelID
	}

	if !ignoreCache(c.flags) {
		if channel, _ := c.client.cache.GetChannel(c.cid); channel != nil {
			return channel, nil
		}
	}

	r := c.client.newRESTRequest(&httd.Request{
		Endpoint: endpoint.Channel(c.cid),
		Ctx:      c.ctx,
	}, c.flags)
	r.pool = c.client.pool.channel
	r.factory = func() interface{} {
		return &Channel{}
	}

	return getChannel(r.Execute)
}

// Update [REST] Update a channel's settings. Returns a channel on success, and a 400 BAD REQUEST
// on invalid parameters. All JSON parameters are optional.
//
//	Method                  PATCH
//	Endpoint                /channels/{channel.id}
//	Discord documentation   https://discord.com/developers/docs/resources/channel#modify-channel
//	Reviewed                2021-08-08
func (c channelQueryBuilder) Update(params *UpdateChannel) (*Channel, error) {
	if params == nil {
		return nil, ErrMissingRESTParams
	}
	if err := c.validate(); err != nil {
		return nil, err
	}

	r := c.client.newRESTRequest(&httd.Request{
		Method:      http.MethodPatch,
		Ctx:         c.ctx,
		Endpoint:    endpoint.Channel(c.cid),
		ContentType: httd.ContentTypeJSON,
		Body:        params,
		Reason:      params.AuditLogReason,
	}, c.flags)
	r.factory = func() interface{} {
		return &Channel{}
	}

	return getChannel(r.Execute)
}

type UpdateChannel struct {
	Name                       *string                    `json:"name,omitempty"`
	Type                       *ChannelType               `json:"type,omitempty"`
	Position                   *uint                      `json:"position,omitempty"`
	Topic                      *string                    `json:"topic,omitempty"`
	NSFW                       *bool                      `json:"nsfw,omitempty"`
	RateLimitPerUser           *uint                      `json:"rate_limit_per_user,omitempty"`
	Bitrate                    *uint                      `json:"bitrate,omitempty"`
	UserLimit                  *uint                      `json:"user_limit,omitempty"`
	PermissionOverwrites       *[]PermissionOverwriteType `json:"permission_overwrites,omitempty"`
	ParentID                   *Snowflake                 `json:"parent_id,omitempty"`
	RTCRegion                  *string                    `json:"rtc_region,omitempty"`
	VideoQualityMode           *VideoQualityMode          `json:"video_quality_mode,omitempty"`
	DefaultAutoArchiveDuration *uint                      `json:"default_auto_archive_duration,omitempty"`

	AuditLogReason string `json:"-"`
}

// Delete [REST] Delete a channel, or close a private message. Requires the 'MANAGE_CHANNELS' permission for
// the guild. Deleting a category does not delete its child Channels; they will have their parent_id removed and a
// Channel Update Gateway event will fire for each of them. Returns a channel object on success.
// Fires a Channel Delete Gateway event.
//
//	Method                  Delete
//	Endpoint                /channels/{channel.id}
//	Discord documentation   https://discord.com/developers/docs/resources/channel#deleteclose-channel
//	Reviewed                2018-10-09
//	Comment                 Deleting a guild channel cannot be undone. Use this with caution, as it
//	                        is impossible to undo this action when performed on a guild channel. In
//	                        contrast, when used with a private message, it is possible to undo the
//	                        action by opening a private message with the recipient again.
func (c channelQueryBuilder) Delete() (channel *Channel, err error) {
	if c.cid.IsZero() {
		return nil, ErrMissingChannelID
	}

	r := c.client.newRESTRequest(&httd.Request{
		Method:   http.MethodDelete,
		Endpoint: endpoint.Channel(c.cid),
		Ctx:      context.Background(),
	}, c.flags)
	r.factory = func() interface{} {
		return &Channel{}
	}

	return getChannel(r.Execute)
}

// TriggerTypingIndicator [REST] Post a typing indicator for the specified channel. Generally bots should not implement
// this route. However, if a bot is responding to a command and expects the computation to take a few seconds, this
// endpoint may be called to let the user know that the bot is processing their message. Returns a 204 empty response
// on success. Fires a Typing Start Gateway event.
//
//	Method                  POST
//	Endpoint                /channels/{channel.id}/typing
//	Discord documentation   https://discord.com/developers/docs/resources/channel#trigger-typing-indicator
//	Reviewed                2018-06-10
//	Comment                 -
func (c channelQueryBuilder) TriggerTypingIndicator() (err error) {
	r := c.client.newRESTRequest(&httd.Request{
		Method:   http.MethodPost,
		Endpoint: endpoint.ChannelTyping(c.cid),
		Ctx:      c.ctx,
	}, c.flags)

	_, err = r.Execute()
	return err
}

// UpdateChannelPermissions https://discord.com/developers/docs/resources/channel#edit-channel-permissions-json-params
type UpdateChannelPermissions struct {
	Allow PermissionBit `json:"allow"` // the bitwise value of all allowed permissions
	Deny  PermissionBit `json:"deny"`  // the bitwise value of all disallowed permissions
	Type  uint          `json:"type"`  // 0=role, 1=member
}

// UpdatePermissions [REST] Edit the channel permission overwrites for a user or role in a channel. Only usable
// for guild Channels. Requires the 'MANAGE_ROLES' permission. Returns a 204 empty response on success.
// For more information about permissions, see permissions.
//
//	Method                  PUT
//	Endpoint                /channels/{channel.id}/permissions/{overwrite.id}
//	Discord documentation   https://discord.com/developers/docs/resources/channel#edit-channel-permissions
//	Reviewed                2018-06-07
//	Comment                 -
func (c channelQueryBuilder) UpdatePermissions(overwriteID Snowflake, params *UpdateChannelPermissions) (err error) {
	if c.cid.IsZero() {
		return ErrMissingChannelID
	}
	if overwriteID.IsZero() {
		return ErrMissingPermissionOverwriteID
	}

	r := c.client.newRESTRequest(&httd.Request{
		Method:      http.MethodPut,
		Ctx:         c.ctx,
		Endpoint:    endpoint.ChannelPermission(c.cid, overwriteID),
		ContentType: httd.ContentTypeJSON,
		Body:        params,
	}, c.flags)

	_, err = r.Execute()
	return err
}

// GetInvites [REST] Returns a list of invite objects (with invite metadata) for the channel. Only usable for
// guild Channels. Requires the 'MANAGE_CHANNELS' permission.
//
//	Method                  GET
//	Endpoint                /channels/{channel.id}/invites
//	Discord documentation   https://discord.com/developers/docs/resources/channel#get-channel-invites
//	Reviewed                2018-06-07
//	Comment                 -
func (c channelQueryBuilder) GetInvites() (invites []*Invite, err error) {
	if c.cid.IsZero() {
		return nil, ErrMissingChannelID
	}

	r := c.client.newRESTRequest(&httd.Request{
		Endpoint: endpoint.ChannelInvites(c.cid),
		Ctx:      c.ctx,
	}, c.flags)
	r.factory = func() interface{} {
		tmp := make([]*Invite, 0)
		return &tmp
	}

	return getInvites(r.Execute)
}

// CreateInvite https://discord.com/developers/docs/resources/channel#create-channel-invite
func (c channelQueryBuilder) CreateInvite(params *CreateInvite) (*Invite, error) {
	if params == nil {
		return nil, ErrMissingRESTParams
	}

	r := c.client.newRESTRequest(&httd.Request{
		Method:      http.MethodPost,
		Ctx:         c.ctx,
		Endpoint:    endpoint.ChannelInvites(c.cid),
		ContentType: httd.ContentTypeJSON,
		Body:        params,
		Reason:      params.AuditLogReason,
	}, c.flags)
	r.factory = func() interface{} {
		return &Invite{}
	}

	return getInvite(r.Execute)
}

type CreateInvite struct {
	MaxAge              int       `json:"max_age"`
	MaxUses             int       `json:"max_uses,omitempty"`
	Temporary           bool      `json:"temporary,omitempty"`
	Unique              bool      `json:"unique,omitempty"`
	TargetType          int       `json:"target_type,omitempty"`
	TargetUserID        Snowflake `json:"target_user_id,omitempty"`
	TargetApplicationID Snowflake `json:"target_application_id,omitempty"`

	AuditLogReason string `json:"-"`
}

// DeletePermission [REST] Delete a channel permission overwrite for a user or role in a channel. Only usable
// for guild Channels. Requires the 'MANAGE_ROLES' permission. Returns a 204 empty response on success. For more
// information about permissions, see permissions: https://discord.com/developers/docs/topics/permissions#permissions
//
//	Method                  DELETE
//	Endpoint                /channels/{channel.id}/permissions/{overwrite.id}
//	Discord documentation   https://discord.com/developers/docs/resources/channel#delete-channel-permission
//	Reviewed                2018-06-07
//	Comment                 -
func (c channelQueryBuilder) DeletePermission(overwriteID Snowflake) (err error) {
	if c.cid.IsZero() {
		return ErrMissingChannelID
	}
	if overwriteID.IsZero() {
		return ErrMissingPermissionOverwriteID
	}

	r := c.client.newRESTRequest(&httd.Request{
		Method:   http.MethodDelete,
		Endpoint: endpoint.ChannelPermission(c.cid, overwriteID),
		Ctx:      c.ctx,
	}, c.flags)

	_, err = r.Execute()
	return err
}

// GroupDMParticipant Information needed to add a recipient to a group chat
type GroupDMParticipant struct {
	AccessToken string    `json:"access_token"`   // access token of a user that has granted your app the gdm.join scope
	Nickname    string    `json:"nick,omitempty"` // nickname of the user being added
	UserID      Snowflake `json:"-"`
}

func (g *GroupDMParticipant) FindErrors() error {
	if g.UserID.IsZero() {
		return ErrMissingUserID
	}
	if g.AccessToken == "" {
		return errors.New("missing access token")
	}
	if err := ValidateUsername(g.Nickname); err != nil && g.Nickname != "" {
		return err
	}

	return nil
}

// AddDMParticipant [REST] Adds a recipient to a Group DM using their access token. Returns a 204 empty response
// on success.
//
//	Method                  PUT
//	Endpoint                /channels/{channel.id}/recipients/{user.id}
//	Discord documentation   https://discord.com/developers/docs/resources/channel#group-dm-add-recipient
//	Reviewed                2018-06-10
//	Comment                 -
func (c channelQueryBuilder) AddDMParticipant(participant *GroupDMParticipant) error {
	if c.cid.IsZero() {
		return ErrMissingChannelID
	}
	if participant == nil {
		return errors.New("params can not be nil")
	}
	if err := participant.FindErrors(); err != nil {
		return err
	}

	r := c.client.newRESTRequest(&httd.Request{
		Method:      http.MethodPut,
		Ctx:         c.ctx,
		Endpoint:    endpoint.ChannelRecipient(c.cid, participant.UserID),
		Body:        participant,
		ContentType: httd.ContentTypeJSON,
	}, c.flags)

	_, err := r.Execute()
	return err
}

// KickParticipant [REST] Removes a recipient from a Group DM. Returns a 204 empty response on success.
//
//	Method                  DELETE
//	Endpoint                /channels/{channel.id}/recipients/{user.id}
//	Discord documentation   https://discord.com/developers/docs/resources/channel#group-dm-remove-recipient
//	Reviewed                2018-06-10
//	Comment                 -
func (c channelQueryBuilder) KickParticipant(userID Snowflake) (err error) {
	if c.cid.IsZero() {
		return ErrMissingChannelID
	}
	if userID.IsZero() {
		return ErrMissingUserID
	}

	r := c.client.newRESTRequest(&httd.Request{
		Method:   http.MethodDelete,
		Endpoint: endpoint.ChannelRecipient(c.cid, userID),
		Ctx:      c.ctx,
	}, c.flags)

	_, err = r.Execute()
	return err
}

// GetMessages https://discord.com/developers/docs/resources/channel#get-channel-messages-query-string-params
// TODO: ensure limits
type GetMessages struct {
	Around Snowflake `urlparam:"around,omitempty"`
	Before Snowflake `urlparam:"before,omitempty"`
	After  Snowflake `urlparam:"after,omitempty"`
	Limit  uint      `urlparam:"limit,omitempty"`
}

func (g *GetMessages) Validate() error {
	var mutuallyExclusives int
	if !g.Around.IsZero() {
		mutuallyExclusives++
	}
	if !g.Before.IsZero() {
		mutuallyExclusives++
	}
	if !g.After.IsZero() {
		mutuallyExclusives++
	}

	if mutuallyExclusives > 1 {
		return errors.New(`only one of the keys "around", "before" and "after" can be set at the time`)
	}
	return nil
}

var _ URLQueryStringer = (*GetMessages)(nil)

// getMessages [REST] Returns the messages for a channel. If operating on a guild channel, this endpoint requires
// the 'VIEW_CHANNEL' permission to be present on the current user. If the current user is missing
// the 'READ_MESSAGE_HISTORY' permission in the channel then this will return no messages
// (since they cannot read the message history). Returns an array of message objects on success.
//
//	Method                  GET
//	Endpoint                /channels/{channel.id}/messages
//	Discord documentation   https://discord.com/developers/docs/resources/channel#get-channel-messages
//	Reviewed                2018-06-10
//	Comment                 The before, after, and around keys are mutually exclusive, only one may
//	                        be passed at a time. see ReqGetChannelMessagesParams.
func (c channelQueryBuilder) getMessages(params URLQueryStringer) (ret []*Message, err error) {
	if c.cid.IsZero() {
		return nil, ErrMissingChannelID
	}

	var query string
	if params != nil {
		query += params.URLQueryString()
	}

	r := c.client.newRESTRequest(&httd.Request{
		Endpoint: endpoint.ChannelMessages(c.cid) + query,
		Ctx:      c.ctx,
	}, c.flags)
	r.factory = func() interface{} {
		tmp := make([]*Message, 0)
		return &tmp
	}

	return getMessages(r.Execute)
}

// GetMessages bypasses discord limitations and iteratively fetches messages until the set filters are met.
func (c channelQueryBuilder) GetMessages(filter *GetMessages) (messages []*Message, err error) {
	// discord values
	const filterLimit = 100
	const filterDefault = 50

	if err = filter.Validate(); err != nil {
		return nil, err
	}

	if filter.Limit == 0 {
		filter.Limit = filterDefault
		// we hardcode it here in case discord goes dumb and decided to randomly change it.
		// This avoids that the bot do not experience a new, random, behaviour on API changes
	}

	if filter.Limit <= filterLimit {
		return c.getMessages(filter)
	}

	latestSnowflake := func(msgs []*Message) (latest Snowflake) {
		for i := range msgs {
			// if msgs[i].ID.Date().After(latest.Date()) {
			if msgs[i].ID > latest {
				latest = msgs[i].ID
			}
		}
		return
	}
	earliestSnowflake := func(msgs []*Message) (earliest Snowflake) {
		for i := range msgs {
			// if msgs[i].ID.Date().Before(earliest.Date()) {
			if msgs[i].ID < earliest {
				earliest = msgs[i].ID
			}
		}
		return
	}

	// scenario#1: filter.Around is not 0 AND filter.Limit is above 100
	//  divide the limit by half and use .Before and .After tags on each quotient limit.
	//  Use the .After on potential remainder.
	//  Note! This method can be used recursively
	if !filter.Around.IsZero() {
		beforeParams := *filter
		beforeParams.Before = beforeParams.Around
		beforeParams.Around = 0
		beforeParams.Limit = filter.Limit / 2
		befores, err := c.GetMessages(&beforeParams)
		if err != nil {
			return nil, err
		}
		messages = append(messages, befores...)

		afterParams := *filter
		afterParams.After = afterParams.Around
		afterParams.Around = 0
		afterParams.Limit = filter.Limit / 2
		afters, err := c.GetMessages(&afterParams)
		if err != nil {
			return nil, err
		}
		messages = append(messages, afters...)

		// filter.Around includes the given ID, so should .Before and .After iterations do as well
		if msg, _ := c.Message(filter.Around).WithContext(c.ctx).Get(); msg != nil {
			// assumption: error here can be caused by the message ID not actually being a real message
			//             and that it was used to get messages in the vicinity. Therefore the err is ignored.
			// TODO: const discord errors.
			messages = append(messages, msg)
		}
	} else {
		// scenario#3: filter.After or filter.Before is set.
		// note that none might be set, which will cause filter.Before to be set after the first 100 messages.
		//
		for {
			if filter.Limit <= 0 {
				break
			}

			f := *filter
			if f.Limit > 100 {
				f.Limit = 100
			}
			filter.Limit -= f.Limit
			msgs, err := c.getMessages(&f)
			if err != nil {
				return nil, err
			}
			messages = append(messages, msgs...)
			if !filter.After.IsZero() {
				filter.After = latestSnowflake(msgs)
			} else {
				// no snowflake or filter.Before
				filter.Before = earliestSnowflake(msgs)
			}
		}
	}

	// duplicates should not exist as we use snowflakes to fetch unique segments in time
	return messages, nil
}

// DeleteMessages https://discord.com/developers/docs/resources/channel#bulk-delete-messages-json-params
type DeleteMessages struct {
	Messages []Snowflake `json:"messages"`
	m        sync.RWMutex
}

func (p *DeleteMessages) tooMany(messages int) (err error) {
	if messages > 100 {
		err = errors.New("must be 100 or less messages to delete")
	}

	return
}

func (p *DeleteMessages) tooFew(messages int) (err error) {
	if messages < 2 {
		err = errors.New("must be at least two messages to delete")
	}

	return
}

// Valid validates the DeleteMessages data
func (p *DeleteMessages) Valid() (err error) {
	p.m.RLock()
	defer p.m.RUnlock()

	messages := len(p.Messages)
	if err = p.tooMany(messages); err != nil {
		return
	}
	err = p.tooFew(messages)
	return
}

// AddMessage Adds a message to be deleted
func (p *DeleteMessages) AddMessage(msg *Message) (err error) {
	p.m.Lock()
	defer p.m.Unlock()

	if err = p.tooMany(len(p.Messages) + 1); err != nil {
		return
	}

	// TODO: check for duplicates as those are counted only once

	p.Messages = append(p.Messages, msg.ID)
	return
}

// DeleteMessages [REST] Delete multiple messages in a single request. This endpoint can only be used on guild
// Channels and requires the 'MANAGE_MESSAGES' permission. Returns a 204 empty response on success. Fires multiple
// Message Delete Gateway events.Any message IDs given that do not exist or are invalid will count towards
// the minimum and maximum message count (currently 2 and 100 respectively). Additionally, duplicated IDs
// will only be counted once.
//
//	Method                  POST
//	Endpoint                /channels/{channel.id}/messages/bulk_delete
//	Discord documentation   https://discord.com/developers/docs/resources/channel#delete-message
//	Reviewed                2018-06-10
//	Comment                 This endpoint will not delete messages older than 2 weeks, and will fail if any message
//	                        provided is older than that.
func (c channelQueryBuilder) DeleteMessages(params *DeleteMessages) (err error) {
	if c.cid.IsZero() {
		return ErrMissingChannelID
	}
	if err = params.Valid(); err != nil {
		return err
	}

	r := c.client.newRESTRequest(&httd.Request{
		Method:      http.MethodPost,
		Ctx:         c.ctx,
		Endpoint:    endpoint.ChannelMessagesBulkDelete(c.cid),
		ContentType: httd.ContentTypeJSON,
		Body:        params,
	}, c.flags)

	_, err = r.Execute()
	return err
}

// AllowedMentions allows finer control over mentions in a message, see
// https://discord.com/developers/docs/resources/channel#allowed-mentions-object for more info.
// Any strings in the Parse value must be any from ["everyone", "users", "roles"].
type AllowedMentions struct {
	Parse       []string    `json:"parse"` // this is purposefully not marked as omitempty as to allow `parse: []` which blocks all mentions.
	Roles       []Snowflake `json:"roles,omitempty"`
	Users       []Snowflake `json:"users,omitempty"`
	RepliedUser bool        `json:"replied_user,omitempty"`
}

// CreateMessageFile contains the information needed to upload a file to Discord, it is part of the
// CreateMessage struct.
type CreateMessageFile struct {
	Reader   io.Reader `json:"-"` // always omit as we don't want this as part of the JSON payload
	FileName string    `json:"-"`

	// SpoilerTag lets discord know that this image should be blurred out.
	// Current Discord behaviour is that whenever a message with one or more images is marked as
	// spoiler tag, all the images in that message are blurred out. (independent of msg.Content)
	SpoilerTag bool `json:"-"`
}

// write helper for file uploading in messages
func (f *CreateMessageFile) write(i int, mp *multipart.Writer) error {
	var filename string
	if f.SpoilerTag {
		filename = AttachmentSpoilerPrefix + f.FileName
	} else {
		filename = f.FileName
	}
	w, err := mp.CreateFormFile("file"+strconv.FormatInt(int64(i), 10), filename)
	if err != nil {
		return err
	}

	if _, err = io.Copy(w, f.Reader); err != nil {
		return err
	}

	return nil
}

// CreateMessage JSON params for CreateChannelMessage
type CreateMessage struct {
	Content    string              `json:"content"`
	Nonce      string              `json:"nonce,omitempty"` // THIS IS A STRING. NOT A SNOWFLAKE! DONT TOUCH!
	Tts        bool                `json:"tts,omitempty"`
	Embeds     []*Embed            `json:"embeds,omitempty"`
	Components []*MessageComponent `json:"components"`
	Files      []CreateMessageFile `json:"-"` // Always omit as this is included in multipart, not JSON payload

	SpoilerTagContent        bool `json:"-"`
	SpoilerTagAllAttachments bool `json:"-"`

	AllowedMentions  *AllowedMentions  `json:"allowed_mentions,omitempty"` // The allowed mentions object for the message.
	MessageReference *MessageReference `json:"message_reference,omitempty"`

	// Deprecated: use Embeds
	Embed *Embed `json:"embed,omitempty"`
}

func (p *CreateMessage) prepare() (postBody interface{}, contentType string, err error) {
	// spoiler tag
	if p.SpoilerTagContent && len(p.Content) > 0 {
		p.Content = "|| " + p.Content + " ||"
	}

	if len(p.Files) == 0 {
		postBody = p
		contentType = httd.ContentTypeJSON
		return
	}

	if p.SpoilerTagAllAttachments {
		for i := range p.Files {
			p.Files[i].SpoilerTag = true
		}
	}

	if p.Embed != nil {
		// check for spoilers
		for i := range p.Files {
			if p.Files[i].SpoilerTag && strings.Contains(p.Embed.Image.URL, p.Files[i].FileName) {
				s := strings.Split(p.Embed.Image.URL, p.Files[i].FileName)
				if len(s) > 0 {
					s[0] += AttachmentSpoilerPrefix + p.Files[i].FileName
					p.Embed.Image.URL = strings.Join(s, "")
				}
			}
		}
	}

	// Set up a new multipart writer, as we'll be using this for the POST body instead
	buf := new(bytes.Buffer)
	mp := multipart.NewWriter(buf)

	// Write the existing JSON payload
	var payload []byte
	payload, err = json.Marshal(p)
	if err != nil {
		return
	}
	if err = mp.WriteField("payload_json", string(payload)); err != nil {
		return
	}

	// Iterate through all the files and write them to the multipart blob
	for i, file := range p.Files {
		if err = file.write(i, mp); err != nil {
			return
		}
	}

	mp.Close()

	postBody = buf
	contentType = mp.FormDataContentType()

	return
}

// CreateMessage [REST] Post a message to a guild text or DM channel. If operating on a guild channel, this
// endpoint requires the 'SEND_MESSAGES' permission to be present on the current user. If the tts field is set to true,
// the SEND_TTS_MESSAGES permission is required for the message to be spoken. Returns a message object. Fires a
// Message Create Gateway event. See message formatting for more information on how to properly format messages.
// The maximum request size when sending a message is 8MB.
//
//	Method                  POST
//	Endpoint                /channels/{channel.id}/messages
//	Discord documentation   https://discord.com/developers/docs/resources/channel#create-message
//	Reviewed                2018-06-10
//	Comment                 Before using this endpoint, you must connect to and identify with a gateway at least once.
func (c channelQueryBuilder) CreateMessage(params *CreateMessage) (ret *Message, err error) {
	if c.cid.IsZero() {
		return nil, ErrMissingChannelID
	}
	if params == nil {
		err = errors.New("message must be set")
		return nil, err
	}

	var (
		postBody    interface{}
		contentType string
	)

	if postBody, contentType, err = params.prepare(); err != nil {
		return nil, err
	}

	r := c.client.newRESTRequest(&httd.Request{
		Method:      http.MethodPost,
		Ctx:         c.ctx,
		Endpoint:    "/channels/" + c.cid.String() + "/messages",
		Body:        postBody,
		ContentType: contentType,
	}, c.flags)
	r.pool = c.client.pool.message
	r.factory = func() interface{} {
		return &Message{}
	}

	return getMessage(r.Execute)
}

// GetPinnedMessages [REST] Returns all pinned messages in the channel as an array of message objects.
//
//	Method                  GET
//	Endpoint                /channels/{channel.id}/pins
//	Discord documentation   https://discord.com/developers/docs/resources/channel#get-pinned-messages
//	Reviewed                2018-06-10
//	Comment                 -
func (c channelQueryBuilder) GetPinnedMessages() (ret []*Message, err error) {
	r := c.client.newRESTRequest(&httd.Request{
		Endpoint: endpoint.ChannelPins(c.cid),
		Ctx:      c.ctx,
	}, c.flags)
	r.factory = func() interface{} {
		tmp := make([]*Message, 0)
		return &tmp
	}

	return getMessages(r.Execute)
}

// CreateWebhook json params for the create webhook rest request avatar string
// https://discord.com/developers/docs/resources/user#avatar-data
type CreateWebhook struct {
	Name   string `json:"name"`   // name of the webhook (2-32 characters)
	Avatar string `json:"avatar"` // avatar data uri scheme, image for the default webhook avatar

	// Reason is a X-Audit-Log-Reason header field that will show up on the audit log for this action.
	Reason string `json:"-"`
}

func (c *CreateWebhook) FindErrors() error {
	if c.Name == "" {
		return ErrMissingWebhookName
	}
	if !(2 <= len(c.Name) && len(c.Name) <= 32) {
		return fmt.Errorf("webhook name must be 2 to 32 characters long: %w", ErrIllegalValue)
	}
	return nil
}

// CreateWebhook [REST] Create a new webhook. Requires the 'MANAGE_WEBHOOKS' permission.
// Returns a webhook object on success.
//
//	Method                  POST
//	Endpoint                /channels/{channel.id}/webhooks
//	Discord documentation   https://discord.com/developers/docs/resources/webhook#create-webhook
//	Reviewed                2018-08-14
//	Comment                 -
func (c channelQueryBuilder) CreateWebhook(params *CreateWebhook) (ret *Webhook, err error) {
	if params == nil {
		return nil, errors.New("params was nil")
	}
	if err = params.FindErrors(); err != nil {
		return nil, err
	}

	r := c.client.newRESTRequest(&httd.Request{
		Method:      http.MethodPost,
		Ctx:         c.ctx,
		Endpoint:    endpoint.ChannelWebhooks(c.cid),
		Body:        params,
		ContentType: httd.ContentTypeJSON,
		Reason:      params.Reason,
	}, c.flags)
	r.factory = func() interface{} {
		return &Webhook{}
	}

	return getWebhook(r.Execute)
}

// GetWebhooks [REST] Returns a list of channel webhook objects. Requires the 'MANAGE_WEBHOOKS' permission.
//
//	Method                  POST
//	Endpoint                /channels/{channel.id}/webhooks
//	Discord documentation   https://discord.com/developers/docs/resources/webhook#get-channel-webhooks
//	Reviewed                2018-08-14
//	Comment                 -
func (c channelQueryBuilder) GetWebhooks() (ret []*Webhook, err error) {
	r := c.client.newRESTRequest(&httd.Request{
		Endpoint: endpoint.ChannelWebhooks(c.cid),
		Ctx:      c.ctx,
	}, c.flags)
	r.factory = func() interface{} {
		tmp := make([]*Webhook, 0)
		return &tmp
	}

	return getWebhooks(r.Execute)
}

// CreateThread https://discord.com/developers/docs/resources/channel#start-thread-without-message
func (c channelQueryBuilder) CreateThread(params *CreateThreadWithoutMessage) (*Channel, error) {
	if params == nil || params.Name == "" {
		return nil, ErrMissingThreadName
	}

	if l := len(params.Name); !(2 <= l && l <= 100) {
		return nil, fmt.Errorf("thread name must be 2 or more characters and no more than 100 characters: %w", ErrIllegalValue)
	}

	if params.Reason != "" && params.AuditLogReason == "" {
		params.AuditLogReason = params.Reason
	}

	r := c.client.newRESTRequest(&httd.Request{
		Method:      http.MethodPost,
		Ctx:         c.ctx,
		Endpoint:    endpoint.ChannelThreads(c.cid),
		Body:        params,
		ContentType: httd.ContentTypeJSON,
		Reason:      params.AuditLogReason,
	}, c.flags)
	r.factory = func() interface{} {
		return &Channel{}
	}

	return getChannel(r.Execute)
}

// CreateThreadWithoutMessage https://discord.com/developers/docs/resources/channel#start-thread-without-message-json-params
type CreateThreadWithoutMessage struct {
	Name                string                  `json:"name"`
	AutoArchiveDuration AutoArchiveDurationTime `json:"auto_archive_duration,omitempty"`
	// In API v9, type defaults to PRIVATE_THREAD in order to match the behavior when
	// thread documentation was first published. In API v10 this will be changed to be a required field, with no default.
	Type             ChannelType `json:"type,omitempty"`
	Invitable        bool        `json:"invitable,omitempty"`
	RateLimitPerUser int         `json:"rate_limit_per_user,omitempty"`

	// AuditLogReason is an X-Audit-Log-Reason header field that will show up on the audit log for this action.
	AuditLogReason string `json:"-"`

	// Deprecated: use AuditLogReason
	Reason string `json:"-"`
}

// JoinThread https://discord.com/developers/docs/resources/channel#join-thread
func (c channelQueryBuilder) JoinThread() error {
	r := c.client.newRESTRequest(&httd.Request{
		Method:      http.MethodPut,
		Ctx:         c.ctx,
		Endpoint:    endpoint.ChannelThreadMemberCurrentUser(c.cid),
		ContentType: httd.ContentTypeJSON,
	}, c.flags)

	_, err := r.Execute()
	return err
}

// AddThreadMember https://discord.com/developers/docs/resources/channel#add-thread-member
func (c channelQueryBuilder) AddThreadMember(userID Snowflake) error {
	r := c.client.newRESTRequest(&httd.Request{
		Method:   http.MethodPut,
		Ctx:      c.ctx,
		Endpoint: endpoint.ChannelThreadMemberUser(c.cid, userID),
	}, c.flags)

	_, err := r.Execute()
	return err
}

// LeaveThread https://discord.com/developers/docs/resources/channel#leave-thread
func (c channelQueryBuilder) LeaveThread() error {
	r := c.client.newRESTRequest(&httd.Request{
		Method:   http.MethodDelete,
		Ctx:      c.ctx,
		Endpoint: endpoint.ChannelThreadMemberCurrentUser(c.cid),
	}, c.flags)

	_, err := r.Execute()
	return err
}

// RemoveThreadMember https://discord.com/developers/docs/resources/channel#remove-thread-member
func (c channelQueryBuilder) RemoveThreadMember(userID Snowflake) error {
	r := c.client.newRESTRequest(&httd.Request{
		Method:   http.MethodDelete,
		Ctx:      c.ctx,
		Endpoint: endpoint.ChannelThreadMemberUser(c.cid, userID),
	}, c.flags)

	_, err := r.Execute()
	return err
}

// GetThreadMember https://discord.com/developers/docs/resources/channel#get-thread-member
func (c channelQueryBuilder) GetThreadMember(userID Snowflake) (*ThreadMember, error) {
	r := c.client.newRESTRequest(&httd.Request{
		Method:   http.MethodGet,
		Ctx:      c.ctx,
		Endpoint: endpoint.ChannelThreadMemberUser(c.cid, userID),
	}, c.flags)
	r.factory = func() interface{} {
		return &ThreadMember{}
	}

	return getThreadMember(r.Execute)
}

// GetThreadMembers https://discord.com/developers/docs/resources/channel#list-thread-members
func (c channelQueryBuilder) GetThreadMembers() ([]*ThreadMember, error) {
	r := c.client.newRESTRequest(&httd.Request{
		Method:   http.MethodGet,
		Ctx:      c.ctx,
		Endpoint: endpoint.ChannelThreadMembers(c.cid),
	}, c.flags)
	r.factory = func() interface{} {
		tmp := make([]*ThreadMember, 0)
		return &tmp
	}

	return getThreadMembers(r.Execute)
}

// ArchivedThreads https://discord.com/developers/docs/resources/channel#list-public-archived-threads-response-body
type ArchivedThreads struct {
	Threads []*Channel      `json:"threads"`
	Members []*ThreadMember `json:"members"`
	HasMore bool            `json:"has_more"`
}

// GetArchivedThreads https://discord.com/developers/docs/resources/channel#list-public-archived-threads-query-string-params
type GetArchivedThreads struct {
	Before Time `urlparam:"before,omitempty"`
	Limit  int  `urlparam:"limit,omitempty"`
}

var _ URLQueryStringer = (*GetArchivedThreads)(nil)

// GetPublicArchivedThreads https://discord.com/developers/docs/resources/channel#list-public-archived-threads
func (c channelQueryBuilder) GetPublicArchivedThreads(params *GetArchivedThreads) (*ArchivedThreads, error) {
	var query string
	if params != nil {
		query += params.URLQueryString()
	}

	r := c.client.newRESTRequest(&httd.Request{
		Method:      http.MethodGet,
		Ctx:         c.ctx,
		Endpoint:    endpoint.ChannelThreadsArchivedPublic(c.cid) + query,
		ContentType: httd.ContentTypeJSON,
	}, c.flags)
	r.factory = func() interface{} {
		return &ArchivedThreads{
			Threads: make([]*Channel, 0),
			Members: make([]*ThreadMember, 0),
		}
	}

	return getArchivedThreads(r.Execute)
}

// GetPrivateArchivedThreads https://discord.com/developers/docs/resources/channel#list-private-archived-threads
func (c channelQueryBuilder) GetPrivateArchivedThreads(params *GetArchivedThreads) (*ArchivedThreads, error) {
	var query string
	if params != nil {
		query += params.URLQueryString()
	}

	r := c.client.newRESTRequest(&httd.Request{
		Method:      http.MethodGet,
		Ctx:         c.ctx,
		Endpoint:    endpoint.ChannelThreadsArchivedPrivate(c.cid) + query,
		ContentType: httd.ContentTypeJSON,
	}, c.flags)
	r.factory = func() interface{} {
		return &ArchivedThreads{
			Threads: make([]*Channel, 0),
			Members: make([]*ThreadMember, 0),
		}
	}

	return getArchivedThreads(r.Execute)
}

// GetJoinedPrivateArchivedThreads https://discord.com/developers/docs/resources/channel#list-joined-private-archived-threads
func (c channelQueryBuilder) GetJoinedPrivateArchivedThreads(params *GetArchivedThreads) (*ArchivedThreads, error) {
	var query string
	if params != nil {
		query += params.URLQueryString()
	}

	r := c.client.newRESTRequest(&httd.Request{
		Method:      http.MethodGet,
		Ctx:         c.ctx,
		Endpoint:    endpoint.ChannelThreadsCurrentUserArchivedPrivate(c.cid) + query,
		ContentType: httd.ContentTypeJSON,
	}, c.flags)
	r.factory = func() interface{} {
		return &ArchivedThreads{
			Threads: make([]*Channel, 0),
			Members: make([]*ThreadMember, 0),
		}
	}

	return getArchivedThreads(r.Execute)
}
