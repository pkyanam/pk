#import <AppKit/AppKit.h>
#import <Foundation/Foundation.h>
#include <stdlib.h>

char *pk_clipboard_read_json(void) {
    @autoreleasepool {
        NSPasteboard *pasteboard = [NSPasteboard generalPasteboard];
        NSMutableArray<NSString *> *files = [NSMutableArray array];
        NSArray *urls = [pasteboard readObjectsForClasses:@[[NSURL class]]
                                                   options:@{NSPasteboardURLReadingFileURLsOnlyKey: @YES}];
        for (NSURL *url in urls) {
            if ([url isFileURL]) {
                [files addObject:url.path];
                if (files.count > 8) break;
            }
        }

        NSMutableDictionary *result = [NSMutableDictionary dictionaryWithObject:files forKey:@"files"];
        NSString *text = [pasteboard stringForType:NSPasteboardTypeString];
        if (text && [text lengthOfBytesUsingEncoding:NSUTF8StringEncoding] <= 128 * 1024) result[@"text"] = text;
        else if (text) result[@"message"] = @"Clipboard text exceeds the 128 KiB limit";
        NSPasteboardType imageType = nil;
        NSString *mime = nil;
        if (files.count == 0 && [pasteboard availableTypeFromArray:@[NSPasteboardTypePNG]]) {
            imageType = NSPasteboardTypePNG;
            mime = @"image/png";
        } else if (files.count == 0 && [pasteboard availableTypeFromArray:@[NSPasteboardTypeTIFF]]) {
            imageType = NSPasteboardTypeTIFF;
            mime = @"image/tiff";
        }
        if (imageType) {
            NSData *data = [pasteboard dataForType:imageType];
            if (data) {
                if (data.length <= 5 * 1024 * 1024) {
                    result[@"image"] = @{@"type": mime, @"data": [data base64EncodedStringWithOptions:0]};
                } else {
                    result[@"message"] = @"Clipboard image exceeds the 5 MiB limit";
                }
            }
        }

        NSError *error = nil;
        NSData *json = [NSJSONSerialization dataWithJSONObject:result options:0 error:&error];
        if (!json || error) return NULL;
        char *copy = malloc(json.length + 1);
        if (!copy) return NULL;
        memcpy(copy, json.bytes, json.length);
        copy[json.length] = '\0';
        return copy;
    }
}

int pk_clipboard_write_text(const char *bytes, size_t length) {
    @autoreleasepool {
        if (length > 1024 * 1024 || (!bytes && length != 0)) return 0;
        NSString *text = [[NSString alloc] initWithBytes:bytes
                                                 length:length
                                               encoding:NSUTF8StringEncoding];
        if (!text) return 0;
        NSPasteboard *pasteboard = [NSPasteboard generalPasteboard];
        [pasteboard clearContents];
        return [pasteboard setString:text forType:NSPasteboardTypeString] ? 1 : 0;
    }
}

void pk_clipboard_free(void *pointer) { free(pointer); }
