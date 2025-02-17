# -*- coding: utf-8 -*-
# Generated by the protocol buffer compiler.  DO NOT EDIT!
# source: coprocess_response_object.proto
"""Generated protocol buffer code."""
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from google.protobuf import reflection as _reflection
from google.protobuf import symbol_database as _symbol_database
# @@protoc_insertion_point(imports)

_sym_db = _symbol_database.Default()




DESCRIPTOR = _descriptor.FileDescriptor(
  name='coprocess_response_object.proto',
  package='coprocess',
  syntax='proto3',
  serialized_options=b'Z\n/coprocess',
  create_key=_descriptor._internal_create_key,
  serialized_pb=b'\n\x1f\x63oprocess_response_object.proto\x12\tcoprocess\"\xdd\x01\n\x0eResponseObject\x12\x13\n\x0bstatus_code\x18\x01 \x01(\x05\x12\x10\n\x08raw_body\x18\x02 \x01(\x0c\x12\x0c\n\x04\x62ody\x18\x03 \x01(\t\x12\x37\n\x07headers\x18\x04 \x03(\x0b\x32&.coprocess.ResponseObject.HeadersEntry\x12-\n\x12multivalue_headers\x18\x05 \x03(\x0b\x32\x11.coprocess.Header\x1a.\n\x0cHeadersEntry\x12\x0b\n\x03key\x18\x01 \x01(\t\x12\r\n\x05value\x18\x02 \x01(\t:\x02\x38\x01\"%\n\x06Header\x12\x0b\n\x03key\x18\x01 \x01(\t\x12\x0e\n\x06values\x18\x02 \x03(\tB\x0cZ\n/coprocessb\x06proto3'
)




_RESPONSEOBJECT_HEADERSENTRY = _descriptor.Descriptor(
  name='HeadersEntry',
  full_name='coprocess.ResponseObject.HeadersEntry',
  filename=None,
  file=DESCRIPTOR,
  containing_type=None,
  create_key=_descriptor._internal_create_key,
  fields=[
    _descriptor.FieldDescriptor(
      name='key', full_name='coprocess.ResponseObject.HeadersEntry.key', index=0,
      number=1, type=9, cpp_type=9, label=1,
      has_default_value=False, default_value=b"".decode('utf-8'),
      message_type=None, enum_type=None, containing_type=None,
      is_extension=False, extension_scope=None,
      serialized_options=None, file=DESCRIPTOR,  create_key=_descriptor._internal_create_key),
    _descriptor.FieldDescriptor(
      name='value', full_name='coprocess.ResponseObject.HeadersEntry.value', index=1,
      number=2, type=9, cpp_type=9, label=1,
      has_default_value=False, default_value=b"".decode('utf-8'),
      message_type=None, enum_type=None, containing_type=None,
      is_extension=False, extension_scope=None,
      serialized_options=None, file=DESCRIPTOR,  create_key=_descriptor._internal_create_key),
  ],
  extensions=[
  ],
  nested_types=[],
  enum_types=[
  ],
  serialized_options=b'8\001',
  is_extendable=False,
  syntax='proto3',
  extension_ranges=[],
  oneofs=[
  ],
  serialized_start=222,
  serialized_end=268,
)

_RESPONSEOBJECT = _descriptor.Descriptor(
  name='ResponseObject',
  full_name='coprocess.ResponseObject',
  filename=None,
  file=DESCRIPTOR,
  containing_type=None,
  create_key=_descriptor._internal_create_key,
  fields=[
    _descriptor.FieldDescriptor(
      name='status_code', full_name='coprocess.ResponseObject.status_code', index=0,
      number=1, type=5, cpp_type=1, label=1,
      has_default_value=False, default_value=0,
      message_type=None, enum_type=None, containing_type=None,
      is_extension=False, extension_scope=None,
      serialized_options=None, file=DESCRIPTOR,  create_key=_descriptor._internal_create_key),
    _descriptor.FieldDescriptor(
      name='raw_body', full_name='coprocess.ResponseObject.raw_body', index=1,
      number=2, type=12, cpp_type=9, label=1,
      has_default_value=False, default_value=b"",
      message_type=None, enum_type=None, containing_type=None,
      is_extension=False, extension_scope=None,
      serialized_options=None, file=DESCRIPTOR,  create_key=_descriptor._internal_create_key),
    _descriptor.FieldDescriptor(
      name='body', full_name='coprocess.ResponseObject.body', index=2,
      number=3, type=9, cpp_type=9, label=1,
      has_default_value=False, default_value=b"".decode('utf-8'),
      message_type=None, enum_type=None, containing_type=None,
      is_extension=False, extension_scope=None,
      serialized_options=None, file=DESCRIPTOR,  create_key=_descriptor._internal_create_key),
    _descriptor.FieldDescriptor(
      name='headers', full_name='coprocess.ResponseObject.headers', index=3,
      number=4, type=11, cpp_type=10, label=3,
      has_default_value=False, default_value=[],
      message_type=None, enum_type=None, containing_type=None,
      is_extension=False, extension_scope=None,
      serialized_options=None, file=DESCRIPTOR,  create_key=_descriptor._internal_create_key),
    _descriptor.FieldDescriptor(
      name='multivalue_headers', full_name='coprocess.ResponseObject.multivalue_headers', index=4,
      number=5, type=11, cpp_type=10, label=3,
      has_default_value=False, default_value=[],
      message_type=None, enum_type=None, containing_type=None,
      is_extension=False, extension_scope=None,
      serialized_options=None, file=DESCRIPTOR,  create_key=_descriptor._internal_create_key),
  ],
  extensions=[
  ],
  nested_types=[_RESPONSEOBJECT_HEADERSENTRY, ],
  enum_types=[
  ],
  serialized_options=None,
  is_extendable=False,
  syntax='proto3',
  extension_ranges=[],
  oneofs=[
  ],
  serialized_start=47,
  serialized_end=268,
)


_HEADER = _descriptor.Descriptor(
  name='Header',
  full_name='coprocess.Header',
  filename=None,
  file=DESCRIPTOR,
  containing_type=None,
  create_key=_descriptor._internal_create_key,
  fields=[
    _descriptor.FieldDescriptor(
      name='key', full_name='coprocess.Header.key', index=0,
      number=1, type=9, cpp_type=9, label=1,
      has_default_value=False, default_value=b"".decode('utf-8'),
      message_type=None, enum_type=None, containing_type=None,
      is_extension=False, extension_scope=None,
      serialized_options=None, file=DESCRIPTOR,  create_key=_descriptor._internal_create_key),
    _descriptor.FieldDescriptor(
      name='values', full_name='coprocess.Header.values', index=1,
      number=2, type=9, cpp_type=9, label=3,
      has_default_value=False, default_value=[],
      message_type=None, enum_type=None, containing_type=None,
      is_extension=False, extension_scope=None,
      serialized_options=None, file=DESCRIPTOR,  create_key=_descriptor._internal_create_key),
  ],
  extensions=[
  ],
  nested_types=[],
  enum_types=[
  ],
  serialized_options=None,
  is_extendable=False,
  syntax='proto3',
  extension_ranges=[],
  oneofs=[
  ],
  serialized_start=270,
  serialized_end=307,
)

_RESPONSEOBJECT_HEADERSENTRY.containing_type = _RESPONSEOBJECT
_RESPONSEOBJECT.fields_by_name['headers'].message_type = _RESPONSEOBJECT_HEADERSENTRY
_RESPONSEOBJECT.fields_by_name['multivalue_headers'].message_type = _HEADER
DESCRIPTOR.message_types_by_name['ResponseObject'] = _RESPONSEOBJECT
DESCRIPTOR.message_types_by_name['Header'] = _HEADER
_sym_db.RegisterFileDescriptor(DESCRIPTOR)

ResponseObject = _reflection.GeneratedProtocolMessageType('ResponseObject', (_message.Message,), {

  'HeadersEntry' : _reflection.GeneratedProtocolMessageType('HeadersEntry', (_message.Message,), {
    'DESCRIPTOR' : _RESPONSEOBJECT_HEADERSENTRY,
    '__module__' : 'coprocess_response_object_pb2'
    # @@protoc_insertion_point(class_scope:coprocess.ResponseObject.HeadersEntry)
    })
  ,
  'DESCRIPTOR' : _RESPONSEOBJECT,
  '__module__' : 'coprocess_response_object_pb2'
  # @@protoc_insertion_point(class_scope:coprocess.ResponseObject)
  })
_sym_db.RegisterMessage(ResponseObject)
_sym_db.RegisterMessage(ResponseObject.HeadersEntry)

Header = _reflection.GeneratedProtocolMessageType('Header', (_message.Message,), {
  'DESCRIPTOR' : _HEADER,
  '__module__' : 'coprocess_response_object_pb2'
  # @@protoc_insertion_point(class_scope:coprocess.Header)
  })
_sym_db.RegisterMessage(Header)


DESCRIPTOR._options = None
_RESPONSEOBJECT_HEADERSENTRY._options = None
# @@protoc_insertion_point(module_scope)
