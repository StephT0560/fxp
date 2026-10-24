# SPDX-License-Identifier: MIT
# Linux entrypoint for QuickFIX C++'s official acceptance definitions/runner.
# The C++ atrun process wrapper is Windows-only; POSIX runat.sh uses Runner.rb.
require './Comparator'
class Comparator
  alias upstream_compare compare
  def compare(expected, actual)
    # Upstream masks checksum/timestamp values. Validate actual framing first,
    # including checksum and exact BodyLength, rather than comparing constants.
    fields = actual.split("\x01")
    unless fields[0] == '8=FIX.4.4' && fields[1].start_with?('9=') && fields[2].start_with?('35=')
      @reason = 'invalid FIX header'; return false
    end
    body = fields[2...-1].join("\x01") + "\x01"
    prefix = fields[0...-1].join("\x01") + "\x01"
    unless fields[1].split('=',2)[1].to_i == body.bytesize && fields[-1] == format('10=%03d', prefix.bytes.sum % 256)
      @reason = 'invalid BodyLength or checksum'; return false
    end
    if expected.split("\x01").any? { |f| f.start_with?('58=') && f.length > 3 } && !fields.any? { |f| f.start_with?('58=') && f.length > 3 }
      @reason = 'required diagnostic Text is missing'; return false
    end
    normalize = lambda do |wire|
      pairs = wire.split("\x01").reject { |f| f.start_with?('9=', '10=', '58=') }
      # FIX permits unordered non-group fields. Preserve an entire repeated
      # body verbatim; never sort repeating-group instances or their members.
      tags = pairs.map { |f| f.split('=',2)[0] }
      pairs.sort_by! { |f| f.split('=',2)[0].to_i } if tags.uniq.size == tags.size
      pairs.map do |f|
        f.start_with?('58=') && f.length > 3 ? '58=diagnostic' : f
      end.join("\x01") + "\x01"
    end
    upstream_compare(normalize.call(expected), normalize.call(actual))
  end
end
load './Runner.rb'
